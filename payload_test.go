package googleplayscraper

import (
	"strings"
	"testing"
)

// Payload-shape tests guard the embedded batchexecute templates against silent
// rot: an emptied file or a renamed/removed placeholder would otherwise only
// surface in the live canary (build-tagged, absent from -short CI), as Google
// answers a malformed body with a NULL payload rather than an error. These are
// pure structural checks — they assert the substitution contract the code
// relies on, NOT that the template still works against Google (that is canary's
// job). They run offline, so there is intentionally no testing.Short() guard.
//
// The tests exercise the embedded variables (listPayloadTemplate,
// qnkhobPayloadTemplate), not the raw files, so they also catch an //go:embed
// directive that drifts from the placeholders the code substitutes.

// payloadPrefix is the form-body prefix every template carries; the code POSTs
// the template verbatim, so the "f.req=" key must already be present.
const payloadPrefix = "f.req="

func TestListPayloadShape(t *testing.T) {
	tmpl := listPayloadTemplate

	if strings.TrimSpace(tmpl) == "" {
		t.Fatal("listPayloadTemplate is empty")
	}
	if !strings.HasPrefix(tmpl, payloadPrefix) {
		t.Errorf("listPayloadTemplate does not start with %q (got %q…)", payloadPrefix, head(tmpl, 16))
	}

	// list.go substitutes exactly these placeholders, each expected once. A
	// renamed or dropped placeholder leaves a literal "__NAME__" in the request
	// body and is caught here.
	for _, ph := range []string{"__NUM__", "__COLLECTION__", "__CATEGORY__"} {
		if n := strings.Count(tmpl, ph); n != 1 {
			t.Errorf("listPayloadTemplate: placeholder %s appears %d times, want 1", ph, n)
		}
	}
}

func TestQnKhObPayloadShape(t *testing.T) {
	tmpl := qnkhobPayloadTemplate

	if strings.TrimSpace(tmpl) == "" {
		t.Fatal("qnkhobPayloadTemplate is empty")
	}
	if !strings.HasPrefix(tmpl, payloadPrefix) {
		t.Errorf("qnkhobPayloadTemplate does not start with %q (got %q…)", payloadPrefix, head(tmpl, 16))
	}

	// qnkhob.go substitutes a single __TOKEN__ where the continuation blob goes.
	if n := strings.Count(tmpl, "__TOKEN__"); n != 1 {
		t.Errorf("qnkhobPayloadTemplate: placeholder __TOKEN__ appears %d times, want 1", n)
	}
}

func head(s string, n int) string {
	if len(s) < n {
		return s
	}
	return s[:n]
}
