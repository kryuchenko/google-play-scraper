package lightfeed

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	gps "github.com/kryuchenko/google-play-scraper"
)

// Default tuning. These favour completeness over speed; a deep scroll of a
// busy category can take tens of seconds.
const (
	defaultScrollRounds = 40
	defaultThrottle     = 600 * time.Millisecond
	defaultTimeout      = 90 * time.Second
)

// ErrLightpandaNotFound is returned by New when WithLightpandaPath points at a
// file that does not exist.
var ErrLightpandaNotFound = errors.New("lightfeed: lightpanda binary not found at the configured path")

// errBadConfig is the base for New's validation failures; callers can match it
// with errors.Is to distinguish configuration errors from runtime ones.
var errBadConfig = errors.New("lightfeed: invalid configuration")

// Paginator is a browser-driven googleplayscraper.FeedPaginator. It scrolls a
// category/cluster page in a headless Lightpanda browser and harvests every app
// link the feed renders.
//
// A Paginator is safe for sequential reuse and reuses a single browser process
// across PaginateFeed calls. It is NOT safe for concurrent use; serialize calls
// or create one Paginator per goroutine. Always Close it to release the browser.
type Paginator struct {
	cfg config

	mu   sync.Mutex // guards proc; serializes PaginateFeed
	proc *managedProc
}

// config holds resolved options. Exactly one of cdpEndpoint / lightpandaPath is
// set after New validates the options.
type config struct {
	cdpEndpoint    string
	lightpandaPath string
	scrollRounds   int
	throttle       time.Duration
	timeout        time.Duration
}

// Option configures a Paginator. Provide exactly one of WithCDPEndpoint or
// WithLightpandaPath.
type Option func(*config)

// WithCDPEndpoint connects to an already-running browser's CDP WebSocket
// endpoint (e.g. "ws://127.0.0.1:9222"). Use this when you manage the
// lightpanda process yourself. Mutually exclusive with WithLightpandaPath.
func WithCDPEndpoint(ws string) Option {
	return func(c *config) { c.cdpEndpoint = ws }
}

// WithLightpandaPath autostarts and manages a lightpanda process from the given
// binary path, on a dynamically chosen port. Mutually exclusive with
// WithCDPEndpoint.
func WithLightpandaPath(path string) Option {
	return func(c *config) { c.lightpandaPath = path }
}

// WithScrollRounds caps how many scroll iterations a single page gets. Reaching
// the cap stops pagination even if the feed has not stabilized. Non-positive
// values are ignored.
func WithScrollRounds(n int) Option {
	return func(c *config) {
		if n > 0 {
			c.scrollRounds = n
		}
	}
}

// WithThrottle sets the pause after each scroll, giving the feed time to load
// the next batch. Non-positive values are ignored.
func WithThrottle(d time.Duration) Option {
	return func(c *config) {
		if d > 0 {
			c.throttle = d
		}
	}
}

// WithTimeout bounds the total time spent paginating a single page. Non-positive
// values are ignored.
func WithTimeout(d time.Duration) Option {
	return func(c *config) {
		if d > 0 {
			c.timeout = d
		}
	}
}

// New validates the options and returns a ready Paginator. It does NOT start the
// browser process — that happens lazily on the first PaginateFeed call, so
// constructing a Paginator is cheap and side-effect-free.
//
// Exactly one of WithCDPEndpoint or WithLightpandaPath must be supplied. With
// WithLightpandaPath, the binary's existence is verified up front
// (ErrLightpandaNotFound otherwise).
func New(opts ...Option) (*Paginator, error) {
	cfg := config{
		scrollRounds: defaultScrollRounds,
		throttle:     defaultThrottle,
		timeout:      defaultTimeout,
	}
	for _, opt := range opts {
		opt(&cfg)
	}

	switch {
	case cfg.cdpEndpoint == "" && cfg.lightpandaPath == "":
		return nil, fmt.Errorf("%w: provide WithCDPEndpoint or WithLightpandaPath", errBadConfig)
	case cfg.cdpEndpoint != "" && cfg.lightpandaPath != "":
		return nil, fmt.Errorf("%w: WithCDPEndpoint and WithLightpandaPath are mutually exclusive", errBadConfig)
	}

	if cfg.lightpandaPath != "" {
		if _, err := os.Stat(cfg.lightpandaPath); err != nil {
			return nil, fmt.Errorf("%w: %q: %v", ErrLightpandaNotFound, cfg.lightpandaPath, err)
		}
	}

	return &Paginator{cfg: cfg}, nil
}

// PaginateFeed scrolls req.URL in the headless browser and returns the apps the
// feed rendered, as thin googleplayscraper.SearchResults (AppID + URL, with
// Title/Icon when the DOM exposes them). It implements
// googleplayscraper.FeedPaginator.
//
// The first call starts the browser (when autostarting); subsequent calls reuse
// it. Pagination stops at req.Limit, when the app count stabilizes across a few
// scroll rounds, at the scroll-round cap, or at the configured timeout.
func (p *Paginator) PaginateFeed(ctx context.Context, req gps.FeedRequest) ([]gps.SearchResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	ws, err := p.endpoint(ctx)
	if err != nil {
		return nil, err
	}

	scrollCtx, cancel := context.WithTimeout(ctx, p.cfg.timeout)
	defer cancel()

	return scrollFeed(scrollCtx, ws, req, p.cfg)
}

// endpoint returns the CDP WebSocket URL to connect to, starting (or reusing)
// the managed lightpanda process when autostarting.
func (p *Paginator) endpoint(ctx context.Context) (string, error) {
	if p.cfg.cdpEndpoint != "" {
		return p.cfg.cdpEndpoint, nil
	}
	if p.proc == nil {
		proc, err := startManagedProc(ctx, p.cfg.lightpandaPath)
		if err != nil {
			return "", err
		}
		p.proc = proc
	}
	return p.proc.wsURL, nil
}

// Close shuts down the managed browser process, if one was started. It is safe
// to call multiple times and on a Paginator that never ran. For external
// endpoints (WithCDPEndpoint) it is a no-op.
func (p *Paginator) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.proc == nil {
		return nil
	}
	err := p.proc.stop()
	p.proc = nil
	return err
}
