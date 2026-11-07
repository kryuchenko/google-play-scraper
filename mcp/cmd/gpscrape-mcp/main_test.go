package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadToken(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "token")
	if err := os.WriteFile(file, []byte("from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, []byte(" \n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("GPSCRAPE_MCP_TOKEN", "")
	if got, err := readToken(""); err != nil || got != "" {
		t.Errorf("no token anywhere: %q, %v", got, err)
	}
	if got, err := readToken(file); err != nil || got != "from-file" {
		t.Errorf("file: %q, %v; want the trimmed contents", got, err)
	}
	if _, err := readToken(empty); err == nil {
		t.Error("an empty token file was accepted")
	}
	if _, err := readToken(filepath.Join(dir, "missing")); err == nil {
		t.Error("a missing token file was accepted")
	}

	t.Setenv("GPSCRAPE_MCP_TOKEN", " from-env\n")
	if got, err := readToken(""); err != nil || got != "from-env" {
		t.Errorf("env: %q, %v", got, err)
	}
	if _, err := readToken(file); err == nil {
		t.Error("file and env together were accepted; one source should win only by being the only one")
	}
}
