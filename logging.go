package googleplayscraper

import (
	"crypto/tls"
	"log/slog"
	"net/http/httptrace"
	"sync"
	"time"
)

// Structured logging.
//
// The client is silent unless WithLogger is given a logger, and even then it
// never writes above Debug: nothing that happens inside a request is news to
// the program that asked for it. What it records is the shape of a run --
// which URL was asked, on which attempt, what came back and how long it took,
// why a retry was scheduled, how long the throttle held a request, and every
// change the adaptive controller makes to the interval. That is enough to
// explain a slow run or a blocked address after the fact, which is what a
// debug log is for.
//
// It never records request or response bodies, or headers. A details page is
// a megabyte of markup and a reviews page carries people's names; neither
// belongs in a log that ends up attached to a bug report.
//
// Leaving a logger installed at a level above Debug costs one Enabled call
// per request: attributes are built only after the handler has said it wants
// the record.

// LevelTrace is the level below Debug at which the client records where the
// time inside one request went: DNS, connect, TLS handshake, time to first
// byte, and whether the connection was reused. Debug says what was asked and
// what came back; this says what the network did in between.
const LevelTrace = slog.LevelDebug - 4

// WithLogger installs a structured logger. The client logs at Debug and
// LevelTrace only, and a nil logger leaves it silent.
//
// The logger is called from the goroutine making the request, which may be a
// worker in the pool, so the handler must be safe for concurrent use; the
// standard ones are.
func WithLogger(l *slog.Logger) ClientOption {
	return func(c *Client) {
		if l != nil {
			c.logger = l
		}
	}
}

var discardLogger = slog.New(slog.DiscardHandler)

// log returns the client's logger, or one that discards everything. It is
// nil-safe so a zero Client, which tests construct, behaves like a built one.
func (c *Client) log() *slog.Logger {
	if c.logger == nil {
		return discardLogger
	}
	return c.logger
}

// connTimings collects the httptrace callbacks for one request. The callbacks
// run on whatever goroutine the transport uses, so every field is written
// under the mutex and read once, after the response, under it too.
type connTimings struct {
	mu        sync.Mutex
	start     time.Time
	dnsStart  time.Time
	dnsDone   time.Time
	connStart time.Time
	connDone  time.Time
	tlsStart  time.Time
	tlsDone   time.Time
	gotConn   time.Time
	firstByte time.Time
	reused    bool
	addr      string
}

func newConnTimings() *connTimings {
	return &connTimings{start: time.Now()}
}

func (t *connTimings) mark(field *time.Time) {
	t.mu.Lock()
	*field = time.Now()
	t.mu.Unlock()
}

func (t *connTimings) clientTrace() *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		DNSStart:          func(httptrace.DNSStartInfo) { t.mark(&t.dnsStart) },
		DNSDone:           func(httptrace.DNSDoneInfo) { t.mark(&t.dnsDone) },
		ConnectStart:      func(_, _ string) { t.mark(&t.connStart) },
		ConnectDone:       func(_, _ string, _ error) { t.mark(&t.connDone) },
		TLSHandshakeStart: func() { t.mark(&t.tlsStart) },
		TLSHandshakeDone:  func(tls.ConnectionState, error) { t.mark(&t.tlsDone) },
		GotConn: func(info httptrace.GotConnInfo) {
			t.mu.Lock()
			t.gotConn = time.Now()
			t.reused = info.Reused
			if info.Conn != nil {
				t.addr = info.Conn.RemoteAddr().String()
			}
			t.mu.Unlock()
		},
		GotFirstResponseByte: func() { t.mark(&t.firstByte) },
	}
}

// attrs renders what was observed. Phases that did not happen -- there is no
// lookup or handshake on a reused connection -- are left out rather than
// reported as zero, so the record says what occurred and not what did not.
// It returns nil when nothing fired at all, which is what a transport other
// than net/http's looks like.
func (t *connTimings) attrs() []slog.Attr {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.gotConn.IsZero() && t.firstByte.IsZero() {
		return nil
	}
	attrs := make([]slog.Attr, 0, 8)
	if !t.dnsStart.IsZero() && !t.dnsDone.IsZero() {
		attrs = append(attrs, slog.Duration("dns", t.dnsDone.Sub(t.dnsStart)))
	}
	if !t.connStart.IsZero() && !t.connDone.IsZero() {
		attrs = append(attrs, slog.Duration("connect", t.connDone.Sub(t.connStart)))
	}
	if !t.tlsStart.IsZero() && !t.tlsDone.IsZero() {
		attrs = append(attrs, slog.Duration("tls", t.tlsDone.Sub(t.tlsStart)))
	}
	if !t.gotConn.IsZero() {
		attrs = append(attrs, slog.Bool("reused", t.reused))
	}
	if t.addr != "" {
		attrs = append(attrs, slog.String("addr", t.addr))
	}
	if !t.firstByte.IsZero() {
		attrs = append(attrs, slog.Duration("ttfb", t.firstByte.Sub(t.start)))
	}
	return attrs
}
