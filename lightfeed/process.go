package lightfeed

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"syscall"
	"time"
)

// procReadyTimeout bounds how long startManagedProc waits for lightpanda's CDP
// port to accept connections before giving up.
const procReadyTimeout = 10 * time.Second

// managedProc is a lightpanda process this package started and owns. It serves
// CDP on a dynamically chosen loopback port, reused across PaginateFeed calls.
type managedProc struct {
	cmd   *exec.Cmd
	wsURL string
}

// startManagedProc launches `lightpanda serve` on a free loopback port and waits
// until the CDP endpoint accepts TCP connections.
//
// The process is started with its own context (not the caller's request ctx) so
// it survives across PaginateFeed calls; stop() tears it down. A free port is
// reserved by the OS up front to avoid clashing with other servers, accepting
// the small race window between releasing the probe listener and lightpanda
// binding it.
func startManagedProc(ctx context.Context, binPath string) (*managedProc, error) {
	host := "127.0.0.1"
	port, err := freePort(host)
	if err != nil {
		return nil, fmt.Errorf("lightfeed: reserve port: %w", err)
	}

	// Detach from the request context: the browser outlives a single call.
	cmd := exec.Command(binPath, "serve", "--host", host, "--port", fmt.Sprintf("%d", port))
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("lightfeed: start lightpanda: %w", err)
	}

	addr := net.JoinHostPort(host, fmt.Sprintf("%d", port))
	if err := waitForPort(ctx, addr, procReadyTimeout); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, fmt.Errorf("lightfeed: lightpanda did not become ready on %s: %w", addr, err)
	}

	return &managedProc{
		cmd:   cmd,
		wsURL: fmt.Sprintf("ws://%s", addr),
	}, nil
}

// stop gracefully terminates the process (SIGTERM, then SIGKILL on timeout) and
// reaps it.
func (p *managedProc) stop() error {
	if p.cmd == nil || p.cmd.Process == nil {
		return nil
	}

	_ = p.cmd.Process.Signal(syscall.SIGTERM)

	done := make(chan error, 1)
	go func() { done <- p.cmd.Wait() }()

	select {
	case <-done:
		return nil
	case <-time.After(5 * time.Second):
		_ = p.cmd.Process.Kill()
		<-done
		return nil
	}
}

// freePort asks the OS for an unused TCP port on host by binding port 0 and
// reading back the assigned port, then releasing it for lightpanda to claim.
func freePort(host string) (int, error) {
	l, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// waitForPort polls addr until a TCP connection succeeds, the deadline passes,
// or ctx is cancelled.
func waitForPort(ctx context.Context, addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s: %w", timeout, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
