package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"os"
	"strings"
)

// Resuming the genre pass.
//
// At catalog scale the pass is about an hour of requests, and an interrupted
// one used to start over. It now leaves its first-pass file (genres.tsv.merge)
// and a small state file beside the table, and a rerun on the same snapshot,
// table and storefront picks up after the last id it checkpointed.
//
// What makes that cheap is the walk itself: the merge file holds, in order,
// every row for the ids up to the checkpoint and every table row before them,
// so resuming is truncating it to the checkpointed size, fast-forwarding the
// snapshot and the table past the last id, and appending. The ids that came
// back absent are not stored twice: their A and a marks in the merge file are
// the list the confirmation pass needs.
//
// Only a run over the snapshot resumes. An -ids list is a subset, often piped
// in, and a run over one starts clean.

// genresCheckpointEvery is how many ids pass between checkpoints: at catalog
// scale about 400 of them, each a flush, an fsync and a small JSON write.
var genresCheckpointEvery = 10_000

// genresResume is the state a checkpoint records.
type genresResume struct {
	// The run it belongs to. Any of these differing means the merge file
	// describes another walk, and the run starts clean.
	Snapshot    string `json:"snapshot"`
	SnapshotSHA string `json:"snapshot_sha256"`
	TableSize   int64  `json:"table_size"` // -1: no table
	TableMod    int64  `json:"table_mtime_ns"`
	Lang        string `json:"lang"`
	Country     string `json:"country"`

	// Where it got to. LastID is the last snapshot id whose rows are in the
	// merge file's first Offset bytes; MainDone is set once the whole
	// snapshot is walked and only the confirmation pass is left.
	LastID   string `json:"last_id"`
	MainDone bool   `json:"main_done,omitempty"`
	Offset   int64  `json:"offset"`

	// Counters of the main pass so far, so the summary and the all-failed
	// guard describe the whole pass and not only its last leg.
	Seen    int    `json:"seen"`
	Changed int    `json:"changed"`
	Failed  int    `json:"failed"`
	LastErr string `json:"last_error,omitempty"`
}

func genresResumePath(dir string) string { return genresPath(dir) + ".resume.json" }

// sameRun reports whether s was written by a walk of the same snapshot over
// the same table in the same storefront as cur.
func (s genresResume) sameRun(cur genresResume) bool {
	return s.Snapshot == cur.Snapshot && s.SnapshotSHA == cur.SnapshotSHA &&
		s.TableSize == cur.TableSize && s.TableMod == cur.TableMod &&
		s.Lang == cur.Lang && s.Country == cur.Country
}

// genresRunIdentity is what a resume must match: the snapshot the latest
// manifest names and the table as it is on disk now.
func genresRunIdentity(dir, lang, country string) (genresResume, error) {
	id := genresResume{Lang: lang, Country: country, TableSize: -1}
	if m, ok := latestManifest(dir); ok {
		id.Snapshot, id.SnapshotSHA = m.File, m.SHA256
	}
	st, err := os.Stat(genresPath(dir))
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return id, err
	default:
		id.TableSize, id.TableMod = st.Size(), st.ModTime().UnixNano()
	}
	return id, nil
}

// loadGenresResume returns the saved state if it belongs to cur and its merge
// file is there and at least as long as the checkpoint says.
func loadGenresResume(dir string, cur genresResume) (genresResume, bool) {
	b, err := os.ReadFile(genresResumePath(dir))
	if err != nil {
		return genresResume{}, false
	}
	var s genresResume
	if json.Unmarshal(b, &s) != nil || !s.sameRun(cur) {
		return genresResume{}, false
	}
	st, err := os.Stat(genresPath(dir) + ".merge")
	if err != nil || st.Size() < s.Offset {
		return genresResume{}, false
	}
	return s, true
}

// dropGenresResume removes a run's resume state and merge file.
func dropGenresResume(dir string) {
	_ = os.Remove(genresResumePath(dir))
	_ = os.Remove(genresPath(dir) + ".merge")
}

// resumeTableMerge reopens the merge file of an interrupted walk: cut back to
// offset -- which drops anything written after the checkpoint, a torn line
// included -- with the table cursor moved past lastID, ready to append.
func resumeTableMerge(prev *tableCursor, path string, offset int64, lastID string) (*tableMerge, error) {
	if err := os.Truncate(path, offset); err != nil {
		return nil, err
	}
	// Appending after the cut is writing at offset.
	f, err := openWrite(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return nil, err
	}
	// The rows before lastID, and lastID's own, are in the file already.
	for prev.ok && prev.id <= lastID {
		prev.next()
	}
	if err := prev.err(); err != nil {
		_ = f.Close()
		return nil, err
	}
	m := &tableMerge{prev: prev, f: f, w: bufio.NewWriterSize(f, 1<<20), path: path}
	if lastID != "" {
		m.started, m.last = true, lastID
	}
	return m, nil
}

// checkpoint makes everything written so far durable and returns the size it
// reached, which is where a resume cuts back to.
func (m *tableMerge) checkpoint() (int64, error) {
	if err := m.w.Flush(); err != nil {
		return 0, err
	}
	if err := m.f.Sync(); err != nil {
		return 0, err
	}
	if err := m.stillOurs(); err != nil {
		return 0, err
	}
	st, err := m.f.Stat()
	if err != nil {
		return 0, err
	}
	return st.Size(), nil
}

// absentID is an id the first storefront did not list, with what the table
// said about it.
type absentID struct {
	id, was string
	known   bool
}

// absentFromMerge reads the absent ids back from the first upto bytes of a
// merge file: they are its A and a rows, already in walk order.
func absentFromMerge(path string, upto int64) ([]absentID, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(io.LimitReader(f, upto))
	sc.Buffer(make([]byte, 0, 64<<10), mergeLineMax)
	var out []absentID
	for sc.Scan() {
		mark, rest, _ := strings.Cut(sc.Text(), "\t")
		if mark != markAbsent && mark != markAbsentFirst {
			continue
		}
		id, was, _ := strings.Cut(rest, "\t")
		out = append(out, absentID{id: id, was: was, known: mark == markAbsent})
	}
	return out, sc.Err()
}

// skipThrough drops the ids up to and including last: a resumed walk has
// their rows already.
func skipThrough(ids iter.Seq[string], last string) iter.Seq[string] {
	return func(yield func(string) bool) {
		for id := range ids {
			if id <= last {
				continue
			}
			if !yield(id) {
				return
			}
		}
	}
}

// saveGenresResume writes the state durably (writeJSON is a temp file, fsync
// and rename).
func saveGenresResume(dir string, s genresResume) error {
	if err := writeJSON(genresResumePath(dir), s); err != nil {
		return fmt.Errorf("save genre pass state: %w", err)
	}
	return nil
}
