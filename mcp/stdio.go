package gpsmcp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// maxLine bounds one inbound JSON-RPC message, as the SDK's own stdio
// transport does by default.
const maxLine = 10 << 20

// StdioTransport is the SDK's stdio transport with one difference: a line
// that is not JSON is logged to stderr and skipped. The SDK's reader stops at
// the first undecodable line, which ends the session -- one stray line from a
// client, a shell wrapper or a terminal would take every tool away until the
// client restarts the server. The bad line is not answered with -32700:
// stdout is written by the SDK concurrently, and a line the client cannot
// parse carries no id worth answering.
func StdioTransport(log *slog.Logger) mcp.Transport {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &mcp.IOTransport{
		Reader:        validLines(os.Stdin, log),
		Writer:        nopCloser{os.Stdout},
		MaxLineLength: maxLine,
	}
}

// validLines copies the JSON lines of r to the returned reader and drops the
// rest. Blank lines are dropped silently.
func validLines(r io.Reader, log *slog.Logger) io.ReadCloser {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	pr, pw := io.Pipe()
	go func() {
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 64<<10), maxLine)
		for sc.Scan() {
			line := bytes.TrimSpace(sc.Bytes())
			if len(line) == 0 {
				continue
			}
			if !json.Valid(line) {
				log.Warn("skipped a stdin line that is not JSON", "bytes", len(line))
				continue
			}
			if _, err := pw.Write(append(line, '\n')); err != nil {
				return // the session is gone
			}
		}
		pw.CloseWithError(sc.Err()) // nil at EOF: a clean end of input
	}()
	return pr
}

type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }
