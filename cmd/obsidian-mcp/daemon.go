package main

import (
	"context"
	"log"
	"net"
	"os"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	pb "obsidian-mcp/proto/indexer/v1"
)

// defaultDaemonSocket mirrors cmd/obsidian-indexerd's DefaultSocket so an
// MCP server and a locally-installed daemon find each other by convention
// when no explicit path is set.
const defaultDaemonSocket = "/tmp/obsidian-indexerd.sock"

// dialDaemon returns a gRPC client for the indexer daemon, or nil when
// routing should stay in-process. The OBSIDIAN_MCP_DAEMON env controls
// behavior:
//
//   unset / empty          → no daemon; handlers call internal/ directly.
//   "1", "true", "auto"    → try the default socket; nil on failure.
//   <absolute path>        → dial that specific socket; nil on failure.
//
// Any failure (daemon not running, socket missing, version skew) is
// downgraded to nil so startup never hangs on a missing daemon. The MCP
// handlers see a nil client and silently fall back to in-process.
func dialDaemon() pb.IndexerServiceClient {
	env := os.Getenv("OBSIDIAN_MCP_DAEMON")
	if env == "" {
		return nil
	}
	socket := env
	switch env {
	case "1", "true", "auto":
		socket = defaultDaemonSocket
	}

	// gRPC over Unix domain socket. `passthrough:///` + a custom dialer
	// is the idiomatic way to get gRPC to use anything other than TCP.
	// NewClient (vs the older DialContext) connects lazily — the first
	// RPC surfaces socket-missing errors, which is fine because every
	// routed handler already has an in-process fallback on error.
	conn, err := grpc.NewClient(
		"passthrough:///"+socket,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, addr string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		}),
	)
	if err != nil {
		log.Printf("daemon client setup failed (%s): %v — falling back to in-process", socket, err)
		return nil
	}

	// Cheap liveness probe so the common case (daemon not running) logs a
	// clean fallback message once at startup instead of per-RPC.
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	client := pb.NewIndexerServiceClient(conn)
	if _, err := client.ListNotes(ctx, &pb.ListNotesRequest{VaultPath: "", Limit: 1}); err != nil {
		// An empty vault_path will always return an error from the daemon,
		// but we can distinguish "daemon reachable, returned app error"
		// from "socket unreachable" by the error message. Any response,
		// error or not, confirms the socket is there.
		if isUnreachable(err) {
			log.Printf("daemon unreachable at %s (%v) — falling back to in-process", socket, err)
			_ = conn.Close()
			return nil
		}
	}
	log.Printf("daemon connected at %s — eligible tools will route through it", socket)
	return client
}

// isUnreachable tells apart "socket is not listening" from "daemon
// answered, the request itself was invalid". The stdlib's net errors
// surface as "connection refused" / "no such file or directory" in the
// gRPC error string when the Unix socket path is wrong or the daemon is
// offline.
func isUnreachable(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, needle := range []string{"connection refused", "no such file"} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}
