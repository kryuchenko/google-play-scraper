// Command gpscrape-mcp serves Google Play Store data to AI agents over the
// Model Context Protocol.
//
// With no flags it speaks MCP on stdin/stdout, which is how Claude Code,
// Codex CLI and OpenCode start local servers:
//
//	claude mcp add gpscrape -- gpscrape-mcp
//
// With -http it serves Streamable HTTP at /mcp instead, for clients that
// cannot start a local process:
//
//	gpscrape-mcp -http 127.0.0.1:8080
//	GPSCRAPE_MCP_TOKEN=secret gpscrape-mcp -http :8080
//	gpscrape-mcp -http :8080 -token-file /run/secrets/mcp-token
//
// Binding anything but a loopback address requires a token. There is no
// -token flag: a secret on the command line is visible to every user of the
// machine through ps.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	gpsmcp "github.com/kryuchenko/google-play-scraper/mcp"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "gpscrape-mcp:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		httpAddr    = flag.String("http", "", "serve Streamable HTTP on this address (e.g. 127.0.0.1:8080) instead of stdio")
		tokenFile   = flag.String("token-file", "", "read the bearer token required on /mcp from this file (default: $GPSCRAPE_MCP_TOKEN)")
		secretPath  = flag.Bool("secret-path", false, "also accept the token as the last path segment, /mcp/<token>, for ChatGPT (it cannot send a header)")
		throttle    = flag.Duration("throttle", 300*time.Millisecond, "minimum interval between requests to Google Play")
		concurrency = flag.Int("max-concurrent", 4, "tool calls running at once")
		timeout     = flag.Duration("timeout", 60*time.Second, "time limit for one tool call")
		verbose     = flag.Bool("v", false, "log every tool call to stderr")
		version     = flag.Bool("version", false, "print the version and exit")
	)
	flag.Parse()
	if *version {
		fmt.Println(gpsmcp.Version)
		return nil
	}
	token, err := readToken(*tokenFile)
	if err != nil {
		return err
	}
	if *secretPath && token == "" {
		return errors.New("-secret-path needs a token: set GPSCRAPE_MCP_TOKEN or -token-file")
	}

	// stdout belongs to the protocol in stdio mode: logs go to stderr only.
	level := slog.LevelWarn
	if *verbose {
		level = slog.LevelInfo
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	srv := gpsmcp.NewServer(gpsmcp.NewClient(*throttle), gpsmcp.Options{
		Logger:        logger,
		MaxConcurrent: *concurrency,
		CallTimeout:   *timeout,
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *httpAddr == "" {
		return srv.Run(ctx, gpsmcp.StdioTransport(logger))
	}
	return serveHTTP(ctx, srv, *httpAddr, gpsmcp.HTTPOptions{Token: token, SecretPath: *secretPath})
}

// readToken returns the token from path, or from $GPSCRAPE_MCP_TOKEN when
// path is empty. Setting both is an error rather than a silent precedence
// rule. A token read from a file or a secret store often ends in a newline;
// clients never send one, so it is trimmed.
func readToken(path string) (string, error) {
	env := os.Getenv("GPSCRAPE_MCP_TOKEN")
	if path == "" {
		return strings.TrimSpace(env), nil
	}
	if env != "" {
		return "", errors.New("both -token-file and $GPSCRAPE_MCP_TOKEN are set; use one")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading -token-file: %w", err)
	}
	t := strings.TrimSpace(string(b))
	if t == "" {
		return "", fmt.Errorf("-token-file %s is empty", path)
	}
	return t, nil
}

func serveHTTP(ctx context.Context, srv *mcp.Server, addr string, opts gpsmcp.HTTPOptions) error {
	token := opts.Token
	if err := gpsmcp.CheckListen(addr, token); err != nil {
		return err
	}
	hs := &http.Server{
		Addr:              addr,
		Handler:           gpsmcp.HTTPHandler(srv, opts),
		ReadHeaderTimeout: 10 * time.Second,
		// Requests are small JSON-RPC bodies; a client that takes longer
		// than this to send one is holding a connection, not calling a tool.
		ReadTimeout: 30 * time.Second,
		IdleTimeout: 2 * time.Minute,
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = hs.Shutdown(shutdown)
	}()
	auth := "no token"
	if token != "" {
		auth = "bearer token"
	}
	fmt.Fprintf(os.Stderr, "gpscrape-mcp: serving http://%s/mcp (%s)\n", addr, auth)
	if opts.SecretPath {
		fmt.Fprintf(os.Stderr, "gpscrape-mcp: -secret-path: ChatGPT can use https://<public host>/mcp/<token> with \"No authentication\"; keep that URL out of proxy logs\n")
		if len(token) < 24 {
			fmt.Fprintf(os.Stderr, "gpscrape-mcp: warning: the token is %d characters; in a URL it is the only password, use 32+ random characters (openssl rand -hex 32)\n", len(token))
		}
	}
	if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
