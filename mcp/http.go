package gpsmcp

import (
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// HTTPOptions configure [HTTPHandler].
type HTTPOptions struct {
	// Token, if set, is required on every request to /mcp as
	// "Authorization: Bearer <token>". Surrounding space is ignored.
	Token string
	// SecretPath also admits requests at /mcp/<token>, for ChatGPT, which
	// cannot present an API key (it does OAuth or nothing) but takes any URL;
	// the app is added there with "No authentication". Off by default: a
	// token in a URL ends up in proxy logs, browser history and screenshots,
	// so only a deployment that needs it should accept it. Needs Token.
	SecretPath bool
}

// HTTPHandler serves srv over Streamable HTTP at /mcp, with /healthz for
// probes. With a token a request is admitted with the bearer header and,
// if SecretPath is set, at /mcp/<token>. A wrong secret path answers 404,
// not 401, so a scan learns nothing.
//
// The handler is stateless: each request is a session of its own, so it sits
// behind any load balancer and survives restarts without clients noticing.
// The tools are all one-shot reads, so nothing is lost by it.
func HTTPHandler(srv *mcp.Server, opts HTTPOptions) http.Handler {
	token := strings.TrimSpace(opts.Token)
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, &mcp.StreamableHTTPOptions{
		Stateless:    true,
		JSONResponse: true,
		// A client that cancels or drops a request stops the store requests
		// behind it for 2026-07-28 clients. Legacy HTTP cancellation cannot
		// cross these per-request sessions; the call timeout still bounds it.
		PropagateRequestCancellation: true,
		// The SDK rejects a request that reaches a loopback socket with a
		// foreign Host header: that is what a DNS-rebinding page does to a
		// tokenless server on 127.0.0.1. It is also exactly what a reverse
		// proxy on the same machine does when it forwards Host. With a token
		// the rebinding page has nothing to send, so the check only gets in
		// the way; without one it stays on.
		DisableLocalhostProtection: token != "",
	})
	var protected http.Handler = h
	mux := http.NewServeMux()
	if token != "" {
		protected = bearer(token, h)
		if opts.SecretPath {
			mux.Handle("/mcp/{secret}", secretPath(token, h))
		}
	}
	mux.Handle("/mcp", protected)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("ok\n"))
	})
	return validateOrigin(http.NewCrossOriginProtection().Handler(mux))
}

// validateOrigin enforces MCP's Origin requirement on every method. Go's
// CSRF protection alone skips safe methods and trusts Sec-Fetch-Site before
// inspecting Origin. Native MCP clients normally omit Origin. Browser clients
// may send a single HTTP(S) origin matching the public Host; TLS can terminate
// at a reverse proxy, so the origin scheme need not match r.TLS.
func validateOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origins, present := r.Header["Origin"]; present {
			valid := false
			if len(origins) == 1 {
				o, err := url.Parse(origins[0])
				valid = err == nil && (o.Scheme == "http" || o.Scheme == "https") &&
					o.Host != "" && strings.EqualFold(o.Host, r.Host) && o.User == nil &&
					o.Path == "" && o.RawQuery == "" && !o.ForceQuery && o.Fragment == ""
			}
			if !valid {
				http.Error(w, "invalid Origin", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// bearer admits requests whose Authorization header carries token. The
// scheme is matched case-insensitively (RFC 9110). Both sides are hashed
// before the constant-time compare so the comparison does not leak the
// token's length either.
func bearer(token string, next http.Handler) http.Handler {
	want := sha256.Sum256([]byte(token))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scheme, got, ok := strings.Cut(r.Header.Get("Authorization"), " ")
		gotSum := sha256.Sum256([]byte(strings.TrimSpace(got)))
		if !ok || !strings.EqualFold(scheme, "Bearer") || subtle.ConstantTimeCompare(gotSum[:], want[:]) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="gpscrape"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// secretPath admits requests whose last path segment is the token.
func secretPath(token string, next http.Handler) http.Handler {
	want := sha256.Sum256([]byte(token))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := sha256.Sum256([]byte(r.PathValue("secret")))
		if subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// CheckListen enforces the one rule for HTTP mode: a server reachable from
// other machines must require a token.
func CheckListen(addr, token string) error {
	if strings.TrimSpace(token) == "" && !IsLoopback(addr) {
		return fmt.Errorf("refusing to serve %s without a token: set -token-file or GPSCRAPE_MCP_TOKEN, or bind 127.0.0.1", addr)
	}
	return nil
}

// IsLoopback reports whether a listen address binds only the local machine.
// An empty host (":8080") binds every interface and is not loopback.
func IsLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
