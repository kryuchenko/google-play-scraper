package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"syscall"

	"github.com/kryuchenko/google-play-scraper/records"
)

// ---- gpscrape schema ----

// describing makes common.parse capture its flag set and stop, so `schema`
// can list a command's flags without running it. The flags are registered by
// the command itself, so the description cannot drift from what it accepts.
var (
	describing    bool
	described     *flag.FlagSet
	errDescribing = errors.New("describing")
)

// commandRecords names the shared record each command writes by default
// (with -raw it writes the library's own types instead). Batch commands also
// write records.AppError lines for the ids that failed.
var commandRecords = map[string][]any{
	"app":          {records.App{}, records.AppError{}},
	"search":       {records.AppSummary{}},
	"list":         {records.AppSummary{}},
	"similar":      {records.AppSummary{}},
	"developer":    {records.AppSummary{}},
	"reviews":      {records.Review{}},
	"permissions":  {records.Permissions{}, records.AppError{}},
	"datasafety":   {records.DataSafety{}},
	"availability": {records.Availability{}},
}

type schemaFlag struct {
	Name    string `json:"name"`
	Default string `json:"default,omitempty"`
	Usage   string `json:"usage"`
}

type schemaField struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type schemaRecord struct {
	Name   string        `json:"name"`
	Fields []schemaField `json:"fields"`
}

type schemaCommand struct {
	Command string         `json:"command"`
	Summary string         `json:"summary,omitempty"`
	Flags   []schemaFlag   `json:"flags,omitempty"`
	Records []schemaRecord `json:"records,omitempty"`
}

// cmdSchema writes one object per command: its summary, its flags and the
// fields of the records it writes. `gpscrape schema reviews` describes one.
// It is how an agent learns the interface without parsing help text, and it
// is built from the same flag sets and record types the commands use.
func cmdSchema(args []string) error {
	names := args
	if len(names) == 0 {
		for n := range commands {
			if n != "schema" && n != "mcp" {
				names = append(names, n)
			}
		}
		slices.Sort(names)
	}
	e := newEmitter(os.Stdout)
	defer func() { _ = e.flush() }()
	for _, n := range names {
		var out []schemaCommand
		if n == "catalog" || strings.HasPrefix(n, "catalog ") {
			verbs := []string{strings.TrimSpace(strings.TrimPrefix(n, "catalog"))}
			if verbs[0] == "" {
				verbs = slices.Sorted(func(yield func(string) bool) {
					for v := range catalogVerbs {
						if !yield(v) {
							return
						}
					}
				})
			}
			for _, v := range verbs {
				run, ok := catalogVerbs[v]
				if !ok {
					return records.Invalid("schema: unknown catalog verb %q", v)
				}
				out = append(out, describe("catalog "+v, run))
			}
		} else {
			run, ok := commands[n]
			if !ok {
				return records.Invalid("schema: unknown command %q", n)
			}
			out = append(out, describe(n, run))
		}
		for _, c := range out {
			if err := e.emit(c); err != nil {
				return err
			}
		}
	}
	return e.flush()
}

func describe(name string, run commandFunc) schemaCommand {
	c := schemaCommand{Command: name, Summary: summaryOf(name)}
	describing, described = true, nil
	err := run(nil)
	describing = false
	if errors.Is(err, errDescribing) && described != nil {
		described.VisitAll(func(f *flag.Flag) {
			c.Flags = append(c.Flags, schemaFlag{Name: f.Name, Default: f.DefValue, Usage: f.Usage})
		})
	}
	for _, r := range commandRecords[name] {
		c.Records = append(c.Records, recordSchema(r))
	}
	return c
}

// summaryOf takes the command's line from the usage text.
func summaryOf(name string) string {
	text := usage
	if strings.HasPrefix(name, "catalog ") {
		text, name = catalogUsage, strings.TrimPrefix(name, "catalog ")
	}
	for line := range strings.SplitSeq(text, "\n") {
		t := strings.TrimSpace(line)
		if f := strings.Fields(t); len(f) > 1 && f[0] == name {
			return strings.Join(strings.Fields(t), " ")
		}
	}
	return ""
}

func recordSchema(v any) schemaRecord {
	t := reflect.TypeOf(v)
	r := schemaRecord{Name: t.Name()}
	for i := range t.NumField() {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}
		r.Fields = append(r.Fields, schemaField{Name: name, Type: jsonType(f.Type)})
	}
	return r
}

func jsonType(t reflect.Type) string {
	switch t.Kind() {
	case reflect.Pointer:
		return jsonType(t.Elem())
	case reflect.String:
		return "string"
	case reflect.Bool:
		return "boolean"
	case reflect.Int, reflect.Int64, reflect.Int32:
		return "integer"
	case reflect.Float64, reflect.Float32:
		return "number"
	case reflect.Slice, reflect.Array:
		return "array of " + jsonType(t.Elem())
	case reflect.Map:
		return "object of " + jsonType(t.Elem())
	case reflect.Struct:
		return "object"
	}
	return t.Kind().String()
}

// ---- gpscrape mcp ----

// cmdMCP starts the gpscrape-mcp server, which is a separate binary in its
// own Go module so that this command and the library stay free of the MCP
// SDK. It looks next to this executable first, then on PATH, and passes the
// arguments, stdio and exit status through. It never downloads anything: if
// the server is not installed it says how to install it.
func cmdMCP(args []string) error {
	if describing {
		return errDescribing
	}
	bin := "gpscrape-mcp"
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	path := ""
	if self, err := os.Executable(); err == nil {
		if p := filepath.Join(filepath.Dir(self), bin); fileExists(p) {
			path = p
		}
	}
	if path == "" {
		if p, err := exec.LookPath(bin); err == nil {
			path = p
		}
	}
	if path == "" {
		return records.Errorf(records.CodeNotFound,
			"gpscrape-mcp is not installed. It is a separate binary: "+
				"go install github.com/kryuchenko/google-play-scraper/mcp/cmd/gpscrape-mcp@latest, "+
				"or a prebuilt build from https://github.com/kryuchenko/google-play-scraper/releases")
	}
	if runtime.GOOS != "windows" {
		// Replace this process: stdio, signals and the exit status are the
		// server's own, which is what an MCP client that started
		// `gpscrape mcp` expects.
		return syscall.Exec(path, append([]string{path}, args...), os.Environ())
	}
	// Background on purpose: the server runs until it exits on its own, as it
	// would had this process been replaced, and the client's signals reach it
	// through the console it shares.
	cmd := exec.CommandContext(context.Background(), path, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			exit(ee.ExitCode())
		}
		return fmt.Errorf("running %s: %w", path, err)
	}
	return nil
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
