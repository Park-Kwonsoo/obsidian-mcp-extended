package indexer

import (
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// ChangeKind classifies a FileEvent. Mirrors the proto enum but kept local
// so the watcher package has no reverse dependency on proto/indexer/v1.
// The service layer translates to pb.ChangeKind before sending on the wire.
type ChangeKind int

const (
	ChangeUnspecified ChangeKind = iota
	ChangeCreated
	ChangeModified
	ChangeDeleted
	ChangeRenamed
)

// FileEvent is a per-subscriber notification emitted for every .md mutation
// the watcher observes. Path is vault-relative using forward slashes so it
// lines up with vault.ListMarkdown output across platforms.
type FileEvent struct {
	Kind      ChangeKind
	Path      string
	Timestamp time.Time
}

// Watcher tails the filesystem under a vault root and invalidates any
// cached data the service holds for that vault. One Watcher per vault;
// the service multiplexes them so the same daemon can serve multiple
// vaults simultaneously.
//
// We debounce events because editors (Obsidian included) produce a
// flurry of writes when saving a single note — rename-to-tmp, chmod,
// rename-back. Without debouncing the "invalidate" callback fires
// several times for one logical change, which defeats the point of
// caching.
type Watcher struct {
	Root     string // absolute vault root path
	OnChange func() // invoked (debounced) whenever vault contents change

	fs      *fsnotify.Watcher
	mu      sync.Mutex
	pending *time.Timer
	stop    chan struct{}

	submu       sync.Mutex
	subscribers []chan FileEvent
}

// newWatcher creates but does not start the watcher. Callers register
// OnChange, then call Start.
func newWatcher(root string, onChange func()) (*Watcher, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	return &Watcher{
		Root:     root,
		OnChange: onChange,
		fs:       fsw,
		stop:     make(chan struct{}),
	}, nil
}

// Start walks the vault tree, adds every directory to the inotify/kqueue
// watch set, then enters its event loop. fsnotify doesn't watch
// recursively on its own; we walk up-front and catch new subdirs by
// watching their parent (a Create event on a dir triggers us to add it).
func (w *Watcher) Start() error {
	if err := w.addTree(); err != nil {
		return err
	}
	go w.loop()
	return nil
}

// Close stops the watcher and releases fsnotify resources. Safe to call
// from any goroutine; idempotent.
func (w *Watcher) Close() error {
	close(w.stop)
	return w.fs.Close()
}

// addTree walks the vault and registers every directory with fsnotify.
// Hidden dirs (`.obsidian`, `.trash`, dot-prefixed) are skipped to
// match the walker used by vault.ListMarkdown — otherwise watching
// `.obsidian/workspace` would fire dozens of useless events on every
// pane switch.
func (w *Watcher) addTree() error {
	return filepath.WalkDir(w.Root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		name := d.Name()
		if path != w.Root && (name == ".obsidian" || name == ".trash" || strings.HasPrefix(name, ".")) {
			return filepath.SkipDir
		}
		return w.fs.Add(path)
	})
}

// loop drains fsnotify events. For every relevant event (create/write/
// remove/rename on a .md or a directory) it schedules an OnChange call
// 150 ms in the future, replacing any pending timer. That collapses
// bursts into a single callback.
func (w *Watcher) loop() {
	const debounce = 150 * time.Millisecond
	for {
		select {
		case <-w.stop:
			return
		case ev, ok := <-w.fs.Events:
			if !ok {
				return
			}
			if !relevant(ev) {
				continue
			}
			// A new directory is itself a watch target — add it
			// before letting the debounce timer collapse the event.
			// fsnotify gives absolute paths, so os.Stat is the right
			// probe here; the earlier fs.Stat(dirFS{}, ...) hack was
			// always failing because dirFS.Open always returns
			// fs.ErrNotExist, which meant new subdirs were silently
			// never added and subsequent .md writes under them never
			// invalidated the cache.
			if ev.Op&fsnotify.Create != 0 {
				if info, err := os.Stat(ev.Name); err == nil && info.IsDir() {
					_ = w.fs.Add(ev.Name)
				}
			}
			if kind, ok := classifyEvent(ev); ok {
				w.broadcast(kind, ev.Name)
			}
			w.schedule(debounce)
		case err, ok := <-w.fs.Errors:
			if !ok {
				return
			}
			log.Printf("watcher: %v", err)
		}
	}
}

// schedule (re)arms the debounce timer; on fire it invokes OnChange on
// a fresh goroutine so a slow cache-rebuild can't stall the event loop.
func (w *Watcher) schedule(delay time.Duration) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.pending != nil {
		w.pending.Stop()
	}
	w.pending = time.AfterFunc(delay, func() {
		if w.OnChange != nil {
			w.OnChange()
		}
	})
}

// relevant filters out events we don't care about: chmod-only changes
// on already-watched files and events on non-.md files (attachments,
// images, etc.) that don't affect search results.
func relevant(ev fsnotify.Event) bool {
	if ev.Op == fsnotify.Chmod {
		return false
	}
	// Always honor directory events so new subdirs get added and deleted
	// ones get removed from the watch set.
	if ev.Op&(fsnotify.Create|fsnotify.Remove|fsnotify.Rename) != 0 {
		return true
	}
	return strings.HasSuffix(ev.Name, ".md")
}

// classifyEvent maps a raw fsnotify event into a ChangeKind suitable for
// subscriber delivery. Only .md files produce a kind — directory and
// non-markdown events are swallowed here because subscribers are listening
// for note-level changes, not inode-level noise.
func classifyEvent(ev fsnotify.Event) (ChangeKind, bool) {
	if !strings.HasSuffix(ev.Name, ".md") {
		return ChangeUnspecified, false
	}
	switch {
	case ev.Op&fsnotify.Create != 0:
		return ChangeCreated, true
	case ev.Op&fsnotify.Write != 0:
		return ChangeModified, true
	case ev.Op&fsnotify.Remove != 0:
		return ChangeDeleted, true
	case ev.Op&fsnotify.Rename != 0:
		return ChangeRenamed, true
	}
	return ChangeUnspecified, false
}

// Subscribe registers a new listener and returns its event channel. Buffer
// controls how many events can queue before the watcher drops new ones for
// this subscriber — a slow consumer should not be able to stall the event
// loop. Pass buf<=0 to accept the default (64).
//
// The caller MUST invoke Unsubscribe with the returned channel to release
// resources; the channel is closed at that point so range-loops terminate
// naturally.
func (w *Watcher) Subscribe(buf int) chan FileEvent {
	if buf <= 0 {
		buf = 64
	}
	ch := make(chan FileEvent, buf)
	w.submu.Lock()
	w.subscribers = append(w.subscribers, ch)
	w.submu.Unlock()
	return ch
}

// Unsubscribe removes target from the subscriber set and closes it. Safe
// to call from any goroutine; silently no-ops if target isn't subscribed
// (e.g. already unsubscribed).
func (w *Watcher) Unsubscribe(target chan FileEvent) {
	w.submu.Lock()
	defer w.submu.Unlock()
	for i, s := range w.subscribers {
		if s == target {
			w.subscribers = append(w.subscribers[:i], w.subscribers[i+1:]...)
			close(target)
			return
		}
	}
}

// broadcast fans an event out to every subscriber. Non-blocking sends: a
// subscriber with a full buffer misses this event rather than blocking the
// watcher's event loop. We hold submu across the whole fan-out so
// Unsubscribe (which closes the channel) cannot race with a send.
func (w *Watcher) broadcast(kind ChangeKind, absPath string) {
	rel, err := filepath.Rel(w.Root, absPath)
	if err != nil {
		return
	}
	ev := FileEvent{
		Kind:      kind,
		Path:      filepath.ToSlash(rel),
		Timestamp: time.Now(),
	}
	w.submu.Lock()
	defer w.submu.Unlock()
	for _, ch := range w.subscribers {
		select {
		case ch <- ev:
		default:
			// Slow subscriber: drop rather than stall the loop.
		}
	}
}
