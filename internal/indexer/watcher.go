package indexer

import (
	"io/fs"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

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
	Root   string // absolute vault root path
	OnChange func() // invoked (debounced) whenever vault contents change

	fs      *fsnotify.Watcher
	mu      sync.Mutex
	pending *time.Timer
	stop    chan struct{}
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
			if ev.Op&fsnotify.Create != 0 {
				if info, err := fs.Stat(dirFS{}, ev.Name); err == nil && info.IsDir() {
					_ = w.fs.Add(ev.Name)
				}
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

// dirFS is a tiny adapter so we can call fs.Stat on absolute paths
// without pulling in os.Stat (which would pick up a symlink vs a
// directory inconsistently here). The stdlib's fs.Stat takes an fs.FS;
// we give it one that just forwards to the os.
type dirFS struct{}

func (dirFS) Open(name string) (fs.File, error) { return nil, fs.ErrNotExist }
