package googleplayscraper

import "testing"

func TestExtractWizData(t *testing.T) {
	// Minimal WIZ_global_data block matching the live layout: f.sid (FdrFJe) is a
	// JSON number, bl (cfb2h) a JSON string. Values are the ones captured live on
	// 2026-06-12.
	html := []byte(`<script>window.WIZ_global_data = {"AfY8Hf":false,` +
		`"FdrFJe":"-5981539578575361369","cfb2h":"boq_playuiserver_20260610.06_p0",` +
		`"foo":"bar"};</script>`)

	fsid, bl, ok := extractWizData(html)
	if !ok {
		t.Fatal("ok=false, want true")
	}
	if fsid != "-5981539578575361369" {
		t.Errorf("fsid=%q, want -5981539578575361369", fsid)
	}
	if bl != "boq_playuiserver_20260610.06_p0" {
		t.Errorf("bl=%q, want boq_playuiserver_20260610.06_p0", bl)
	}
}

func TestExtractWizDataNumericFsid(t *testing.T) {
	// Google sometimes serves f.sid as a bare JSON number rather than a string;
	// wizString must accept both.
	html := []byte(`WIZ_global_data = {"FdrFJe":123456789,"cfb2h":"boq_x"};`)
	fsid, bl, ok := extractWizData(html)
	if !ok {
		t.Fatal("ok=false, want true")
	}
	if fsid != "123456789" {
		t.Errorf("fsid=%q, want 123456789", fsid)
	}
	if bl != "boq_x" {
		t.Errorf("bl=%q, want boq_x", bl)
	}
}

func TestExtractWizDataMissing(t *testing.T) {
	cases := map[string][]byte{
		"no block":   []byte(`<html><body>nothing here</body></html>`),
		"no fsid":    []byte(`WIZ_global_data = {"cfb2h":"boq_x"};`),
		"no bl":      []byte(`WIZ_global_data = {"FdrFJe":"1"};`),
		"bad json":   []byte(`WIZ_global_data = {not json};`),
		"empty fsid": []byte(`WIZ_global_data = {"FdrFJe":"","cfb2h":"boq_x"};`),
	}
	for name, html := range cases {
		if _, _, ok := extractWizData(html); ok {
			t.Errorf("%s: ok=true, want false", name)
		}
	}
}
