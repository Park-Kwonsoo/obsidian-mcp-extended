// obsidian-indexerd is the long-lived companion daemon to obsidian-mcp.
// It hosts the full tool-implementation surface as a gRPC service over
// a Unix domain socket, so a running MCP server can offload work to a
// warm process instead of re-scanning the vault on every request.
//
// In P5 the daemon is a thin delegation layer: every RPC calls straight
// into the in-process internal/search, internal/vault, etc. packages —
// so correctness tracks the MCP server line-for-line. The warm index
// and fsnotify watcher that make the daemon *faster* than the MCP
// server land in a follow-up; this phase freezes the contract and
// proves the wire still works.
package main

import (
	"context"
	"flag"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"google.golang.org/grpc"

	"obsidian-mcp/internal/indexer"
	pb "obsidian-mcp/proto/indexer/v1"
)

var version = "dev" // stamped via -ldflags "-X main.version=..."

// DefaultSocket is where the daemon listens and where obsidian-mcp looks
// for it. Unix domain socket path; override with --socket for testing or
// multi-user installs.
const DefaultSocket = "/tmp/obsidian-indexerd.sock"

func main() {
	var (
		socketPath = flag.String("socket", DefaultSocket, "Unix domain socket path")
		showVer    = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()

	if *showVer {
		log.Printf("obsidian-indexerd %s", version)
		return
	}

	// Remove any stale socket — a previous run might have died without
	// cleaning up, and listen would then fail with "address in use".
	_ = os.Remove(*socketPath)

	// Ensure the parent dir exists (matters when someone points --socket
	// at a nested path like ~/.run/obsidian-indexerd.sock).
	if err := os.MkdirAll(filepath.Dir(*socketPath), 0o755); err != nil {
		log.Fatalf("create socket dir: %v", err)
	}

	lis, err := net.Listen("unix", *socketPath)
	if err != nil {
		log.Fatalf("listen %s: %v", *socketPath, err)
	}
	defer lis.Close()

	// 0600 keeps the socket private to the user that launched the daemon —
	// important because every RPC can read arbitrary vault contents.
	if err := os.Chmod(*socketPath, 0o600); err != nil {
		log.Fatalf("chmod socket: %v", err)
	}

	indexerSrv := indexer.NewServer()
	srv := grpc.NewServer()
	pb.RegisterIndexerServiceServer(srv, indexerSrv)
	log.Printf("obsidian-indexerd %s listening on %s", version, *socketPath)

	go func() {
		if err := srv.Serve(lis); err != nil {
			log.Fatalf("serve: %v", err)
		}
	}()

	// Graceful shutdown on SIGINT / SIGTERM — gives in-flight RPCs a
	// chance to finish before the socket goes away.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()

	log.Println("shutting down")
	// GracefulStop blocks until every in-flight RPC returns. Streaming
	// RPCs like SubscribeFileChanges park on their watcher's subscriber
	// channel and only exit when that channel closes — so run
	// GracefulStop (which immediately stops accepting new RPCs)
	// concurrently with indexerSrv.Close(), which closes every watcher
	// and unblocks those streams. Without this wiring GracefulStop
	// hangs forever whenever a subscriber is still attached.
	stopped := make(chan struct{})
	go func() {
		srv.GracefulStop()
		close(stopped)
	}()
	_ = indexerSrv.Close()
	<-stopped
	_ = os.Remove(*socketPath)
}
