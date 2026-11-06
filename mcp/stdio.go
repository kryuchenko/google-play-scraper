package gpsmcp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// maxLine bounds one inbound JSON-RPC message, as the SDK's own stdio
// transport does by default.
const maxLine = 10 << 20

// StdioTransport is the SDK's stdio transport with one difference: a line
// that is not a JSON-RPC message is logged to stderr and skipped. The SDK's
// reader stops at the first line it cannot decode, which ends the session --
// one stray line from a client, a shell wrapper or a terminal would take every
// tool away until the client restarts the server. That covers valid JSON that
// is not a message too ({}, 123, "jsonrpc":"1.0", an object id): the SDK
// ends the session on each. The bad line is not answered with -32700 or
// -32600: stdout is written by the SDK concurrently, and a line the client
// cannot parse carries no id worth answering.
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

// validLines copies the lines of r that decode as JSON-RPC messages -- one
// message, or a non-empty batch of them -- to the returned reader and drops
// the rest. Blank lines are dropped silently.
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
			if err := checkMessage(line); err != nil {
				log.Warn("skipped a stdin line that is not a JSON-RPC message", "bytes", len(line), "err", err)
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

// checkMessage reports whether line decodes the way the SDK's reader will
// decode it: as one JSON-RPC message, or as a non-empty array of them.
func checkMessage(line []byte) error {
	if line[0] != '[' {
		_, err := jsonrpc.DecodeMessage(line)
		return err
	}
	var batch []json.RawMessage
	if err := json.Unmarshal(line, &batch); err != nil {
		return err
	}
	if len(batch) == 0 {
		return errors.New("empty batch")
	}
	for _, m := range batch {
		if _, err := jsonrpc.DecodeMessage(m); err != nil {
			return err
		}
	}
	return nil
}

type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }
