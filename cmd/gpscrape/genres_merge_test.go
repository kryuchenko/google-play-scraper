package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func writeTable(t *testing.T, path string, rows ...string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.Join(rows, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readTable(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimRight(string(b), "\n"), "\n")
}

// One walk of the table alongside the ids settles every kind of row: matched,
// first seen, unmatched (kept, or dropped by -prune), and absent ids that are
// alive elsewhere, unconfirmed, or gone -- with and without a previous row.
func TestTableMergeSettlesEveryRow(t *testing.T) {
	for _, prune := range []bool{false, true} {
		dir := t.TempDir()
		path := filepath.Join(dir, "genres.tsv")
		writeTable(t, path,
			"a.keep\tGAME_PUZZLE",     // unmatched: kept unless prune
			"b.same\tTOOLS",           // matched, same
			"c.alive\tGAME_ARCADE",    // absent, alive elsewhere with a new genre
			"d.unconf\tSHOPPING",      // absent, unconfirmed: keeps its row
			"e.gone\tSOCIAL",          // absent, gone: dropped
			"z.tail\tMUSIC_AND_AUDIO", // unmatched after the last id
		)
		prev, err := openTable(path)
		if err != nil {
			t.Fatal(err)
		}
		m, err := newTableMerge(prev, path+".merge")
		if err != nil {
			t.Fatal(err)
		}
		step := func(id, genre, mark string, wantWas string, wantKnown bool) {
			t.Helper()
			was, known, err := m.lookup(id)
			if err != nil || was != wantWas || known != wantKnown {
				t.Fatalf("lookup(%s) = %q %v %v, want %q %v", id, was, known, err, wantWas, wantKnown)
			}
			if err := m.row(id, genre, mark); err != nil {
				t.Fatal(err)
			}
		}
		step("b.same", "TOOLS", "", "TOOLS", true)
		step("b2.first", "", markAbsentFirst, "", false) // absent, never known, alive with an empty genre
		step("c.alive", "GAME_ARCADE", markAbsent, "GAME_ARCADE", true)
		step("d.unconf", "SHOPPING", markAbsent, "SHOPPING", true)
		step("d2.unconf", "", markAbsentFirst, "", false) // absent, never known, unconfirmed
		step("e.gone", "SOCIAL", markAbsent, "SOCIAL", true)
		step("f.new", "EDUCATION", "", "", false)
		if err := m.finish(); err != nil {
			t.Fatal(err)
		}
		prev.close()

		absent := []string{"b2.first", "c.alive", "d.unconf", "d2.unconf", "e.gone"}
		outcomes := map[string]settled{"b2.first": settledAlive, "c.alive": settledAlive,
			"d.unconf": settledUnconfirmed, "d2.unconf": settledUnconfirmed, "e.gone": settledGone}
		genres := map[string]string{"b2.first": "", "c.alive": "GAME_BOARD"}
		pruned, err := settleTable(m.path, path, absent, func(i int) (settled, string) {
			return outcomes[absent[i]], genres[absent[i]]
		}, prune)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"a.keep\tGAME_PUZZLE", "b.same\tTOOLS", "b2.first\t", "c.alive\tGAME_BOARD",
			"d.unconf\tSHOPPING", "f.new\tEDUCATION", "z.tail\tMUSIC_AND_AUDIO"}
		wantPruned := 0
		if prune {
			want = slices.DeleteFunc(want, func(r string) bool {
				return strings.HasPrefix(r, "a.keep") || strings.HasPrefix(r, "z.tail")
			})
			wantPruned = 2
		}
		if got := readTable(t, path); !slices.Equal(got, want) || pruned != wantPruned {
			t.Errorf("prune=%v: table\n%s\npruned %d\nwant\n%s\npruned %d",
				prune, strings.Join(got, "\n"), pruned, strings.Join(want, "\n"), wantPruned)
		}
	}
}

// The merge reads the table's next row as the answer, which is only right
// for ids in strictly ascending order; anything else is refused, not
// silently misread.
func TestTableMergeRefusesUnsortedIDs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "genres.tsv")
	writeTable(t, path, "a\tX", "b\tY")
	prev, err := openTable(path)
	if err != nil {
		t.Fatal(err)
	}
	defer prev.close()
	m, err := newTableMerge(prev, path+".merge")
	if err != nil {
		t.Fatal(err)
	}
	defer m.abandon()
	if _, _, err := m.lookup("b"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b"} {
		if _, _, err := m.lookup(id); err == nil {
			t.Errorf("lookup(%q) after \"b\" was accepted", id)
		}
	}
}

// A caller's -ids list comes in any order and may repeat; it is sorted and
// deduplicated before the merge.
func TestSortedIDs(t *testing.T) {
	seq, err := sortedIDs(context.Background(), slices.Values([]string{"c", "a", "b", "a", "c"}))
	if got := slices.Collect(seq); err != nil || !slices.Equal(got, []string{"a", "b", "c"}) {
		t.Errorf("sortedIDs = %v, %v", got, err)
	}

	// A list that never ends -- a stdin left open -- still stops on cancel.
	ctx, cancel := context.WithCancel(context.Background())
	block := make(chan struct{})
	defer close(block)
	never := func(yield func(string) bool) {
		if yield("a") {
			<-block
		}
	}
	time.AfterFunc(20*time.Millisecond, cancel)
	if _, err := sortedIDs(ctx, never); !errors.Is(err, context.Canceled) {
		t.Errorf("sortedIDs on a list that never ends: %v, want context.Canceled", err)
	}
}

// The table is walked, not looked up, so a row out of order -- a hand-edited
// table, a duplicate -- is an error, not a quietly wrong answer.
func TestTableCursorRefusesUnsortedTable(t *testing.T) {
	for name, rows := range map[string][]string{
		"unsorted":  {"b\tY", "a\tX"},
		"duplicate": {"a\tX", "a\tY"},
	} {
		path := filepath.Join(t.TempDir(), "genres.tsv")
		writeTable(t, path, rows...)
		prev, err := openTable(path)
		if err != nil {
			t.Fatal(err)
		}
		m, err := newTableMerge(prev, path+".merge")
		if err != nil {
			t.Fatal(err)
		}
		_, _, lerr := m.lookup("a")
		if lerr == nil {
			lerr = m.finish()
		}
		if lerr == nil || !strings.Contains(lerr.Error(), "not sorted") {
			t.Errorf("%s table: %v, want a not-sorted error", name, lerr)
		}
		m.abandon()
		prev.close()
	}
}

// A genre with a tab in it -- not one Google uses, but the table is a text
// file -- is kept whole, and never read as a merge mark.
func TestTableMergeKeepsTabsInGenre(t *testing.T) {
	path := filepath.Join(t.TempDir(), "genres.tsv")
	writeTable(t, path, "a\tX\tP", "b\tY\tA")
	prev, err := openTable(path)
	if err != nil {
		t.Fatal(err)
	}
	m, err := newTableMerge(prev, path+".merge")
	if err != nil {
		t.Fatal(err)
	}
	was, known, err := m.lookup("b") // a stays unmatched
	if err != nil || !known || was != "Y\tA" {
		t.Fatalf("lookup(b) = %q %v %v", was, known, err)
	}
	if err := m.row("b", was, ""); err != nil { // a failed lookup keeps its row
		t.Fatal(err)
	}
	if err := m.finish(); err != nil {
		t.Fatal(err)
	}
	prev.close()
	if _, err := settleTable(m.path, path, nil, nil, false); err != nil {
		t.Fatal(err)
	}
	if got := readTable(t, path); !slices.Equal(got, []string{"a\tX\tP", "b\tY\tA"}) {
		t.Errorf("table = %q", got)
	}
}

// A settlement that does not account for every absent id fails before the
// rename: the good table stays.
func TestSettleTableChecksBeforePublishing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "genres.tsv")
	writeTable(t, path, "a\tX")
	merged := filepath.Join(dir, "merge")
	if err := os.WriteFile(merged, []byte("-\ta\tX\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := settleTable(merged, path, []string{"b"}, func(int) (settled, string) { return settledGone, "" }, false)
	if err == nil {
		t.Fatal("an absent id the first pass never wrote was accepted")
	}
	if got := readTable(t, path); !slices.Equal(got, []string{"a\tX"}) {
		t.Errorf("table replaced after a failed settlement: %q", got)
	}
}

// A missing table reads as empty, so the first run starts from nothing.
func TestOpenTableMissing(t *testing.T) {
	prev, err := openTable(filepath.Join(t.TempDir(), "genres.tsv"))
	if err != nil || prev.ok {
		t.Fatalf("missing table: ok=%v err=%v", prev.ok, err)
	}
	prev.close()
}
