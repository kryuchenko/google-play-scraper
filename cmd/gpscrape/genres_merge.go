package main

import (
	"bufio"
	"errors"
	"fmt"
	"iter"
	"os"
	"slices"
	"strings"
)

// The genre pass walks the snapshot and the genre table together.
//
// Both are sorted by app id, so the previous genre of each id is the next row
// of the table, not a lookup: the pass holds one row of each file and the
// ids that came back absent, instead of the whole table as a map -- which was
// 307MB on the real 3.2M-row table, and 700MB at the peak with the keys
// collected and sorted for the write.
//
// The new table is written in two passes over disk. The first runs alongside
// the digest pass and writes every row in order; the rows it cannot decide
// yet carry a mark. Those are the ids absent from the first storefront, which
// confirmGone settles only after the main pass, and -- with -prune -- the
// table rows no snapshot id matched, which may be dropped only if the run
// turns out not to have failed wholesale. The second pass settles the marks
// and publishes the table with the same temporary-file, fsync and rename the
// writers always used.

const (
	markAbsent      = "A" // absent from the first storefront; the table knew it
	markAbsentFirst = "a" // absent, and the table did not know it
	markUnmatched   = "P" // a table row no snapshot id matched
)

// tableCursor reads the genre table a row at a time, in order.
type tableCursor struct {
	f         *os.File
	sc        *bufio.Scanner
	id, genre string
	ok        bool
}

// openTable opens the table at path; a missing table reads as empty.
func openTable(path string) (*tableCursor, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return &tableCursor{}, nil
	}
	if err != nil {
		return nil, err
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	t := &tableCursor{f: f, sc: sc}
	t.next()
	return t, nil
}

func (t *tableCursor) next() {
	t.ok = false
	if t.sc == nil {
		return
	}
	for t.sc.Scan() {
		id, genre, ok := strings.Cut(t.sc.Text(), "\t")
		if ok {
			t.id, t.genre, t.ok = id, genre, true
			return
		}
	}
}

func (t *tableCursor) err() error {
	if t.sc == nil {
		return nil
	}
	return t.sc.Err()
}

func (t *tableCursor) close() {
	if t.f != nil {
		_ = t.f.Close()
	}
}

// tableMerge writes the first-pass table: every row, in order, with marks on
// the rows the second pass settles.
type tableMerge struct {
	prev    *tableCursor
	f       *os.File
	w       *bufio.Writer
	path    string
	last    string
	started bool
}

func newTableMerge(prev *tableCursor, path string) (*tableMerge, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	return &tableMerge{prev: prev, f: f, w: bufio.NewWriterSize(f, 1<<20), path: path}, nil
}

func (m *tableMerge) row(id, genre, mark string) error {
	var err error
	if mark == "" {
		_, err = fmt.Fprintf(m.w, "%s\t%s\n", id, genre)
	} else {
		_, err = fmt.Fprintf(m.w, "%s\t%s\t%s\n", id, genre, mark)
	}
	return err
}

// lookup returns the table's genre for id and consumes that row, writing the
// rows before it -- ids the snapshot no longer lists -- through unchanged,
// marked as unmatched. Ids must arrive in strictly ascending order: that is
// what makes the table's next row the answer.
func (m *tableMerge) lookup(id string) (was string, known bool, err error) {
	if m.started && id <= m.last {
		return "", false, fmt.Errorf("ids out of order: %q after %q; the genre pass needs a sorted, "+
			"duplicate-free list", id, m.last)
	}
	m.started, m.last = true, id
	for m.prev.ok && m.prev.id < id {
		if err := m.row(m.prev.id, m.prev.genre, markUnmatched); err != nil {
			return "", false, err
		}
		m.prev.next()
	}
	if m.prev.ok && m.prev.id == id {
		was, known = m.prev.genre, true
		m.prev.next()
	}
	return was, known, m.prev.err()
}

// finish writes the table rows after the last id and closes the file.
func (m *tableMerge) finish() error {
	for ; m.prev.ok; m.prev.next() {
		if err := m.row(m.prev.id, m.prev.genre, markUnmatched); err != nil {
			return err
		}
	}
	if err := m.prev.err(); err != nil {
		return err
	}
	if err := m.w.Flush(); err != nil {
		return err
	}
	return m.f.Close()
}

func (m *tableMerge) abandon() {
	_ = m.f.Close()
	_ = os.Remove(m.path)
}

// settled is what the second pass does with one absent id.
type settled int

const (
	settledGone        settled = iota // no storefront has it: drop the row
	settledAlive                      // found elsewhere: write the genre it answered with
	settledUnconfirmed                // not settled: the table keeps what it said
)

// settleTable reads the first-pass file and writes the final table to path:
// marked absent rows take their settlement (absentIDs is ascending and
// matches them in order), unmatched rows are dropped when prune is set, every
// other row is copied. It returns how many rows prune dropped.
func settleTable(merged, path string, absentIDs []string, settle func(i int) (settled, string), prune bool) (int, error) {
	f, err := os.Open(merged)
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)

	pruned, next := 0, 0
	write := func(w *bufio.Writer) error {
		for sc.Scan() {
			line := sc.Text()
			id, rest, _ := strings.Cut(line, "\t")
			genre, mark, marked := strings.Cut(rest, "\t")
			switch {
			case !marked:
				if _, err := fmt.Fprintf(w, "%s\n", line); err != nil {
					return err
				}
				continue
			case mark == markUnmatched:
				if prune {
					pruned++
					continue
				}
			case mark == markAbsent || mark == markAbsentFirst:
				if next >= len(absentIDs) || absentIDs[next] != id {
					return fmt.Errorf("genre table merge: absent id %q out of step", id)
				}
				s, g := settle(next)
				next++
				switch {
				case s == settledAlive:
					genre = g
				case s == settledUnconfirmed && mark == markAbsent:
					// the table keeps what it said
				default:
					continue // gone, or unconfirmed and never in the table
				}
			}
			if _, err := fmt.Fprintf(w, "%s\t%s\n", id, genre); err != nil {
				return err
			}
		}
		return sc.Err()
	}
	if err := writeTableAtomic(path, write); err != nil {
		return 0, err
	}
	if next != len(absentIDs) {
		return 0, fmt.Errorf("genre table merge: %d absent ids, %d settled", len(absentIDs), next)
	}
	return pruned, nil
}

// sortedIDs makes a caller-supplied list fit the merge: sorted, duplicates
// dropped. It holds the list, which is a subset by construction; the
// snapshot itself is already sorted and streams through untouched.
func sortedIDs(ids iter.Seq[string]) iter.Seq[string] {
	all := slices.Sorted(ids)
	return slices.Values(slices.Compact(all))
}
