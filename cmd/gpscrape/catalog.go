package main

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	googleplayscraper "github.com/kryuchenko/google-play-scraper"
	"github.com/kryuchenko/google-play-scraper/records"
)

// The catalog verbs.
//
// Keeping a local copy of Google's app list current is not one job but three,
// and they differ by two orders of magnitude in cost, so they are separate
// commands rather than flags on one:
//
//	check    2 requests      has Google republished?
//	new      17 requests     what has appeared, from the store's own new lists
//	size     ~900 requests   how many there are, to within a stated error
//	genres   ~3.3k requests  what changed genre, and what is gone
//	sweep    83,445 requests the complete list, which is the only way to be sure
//	apps     none            the id list, filtered by genre, from disk
//	diff     none            compare two snapshots that are already on disk
//	ids      as you ask      stream ids without keeping any state
//
// The split follows what each can and cannot see. `genres` re-reads apps you
// already know, so it finds removals and genre changes with a day's
// resolution for a few minutes' work. It cannot find an app you have never
// heard of. `new` finds those, but it reads ranked lists, so an app with no
// traction never appears in one. Only `sweep` is complete, and Google
// republishes the sitemap every four days or so, which bounds how often it is
// worth running.
//
// That is a cheap high-precision signal over a slow complete one, and the
// literature on refresh crawling (Cho & Garcia-Molina; Azar et al.; and for
// noisy signals specifically, arXiv:2502.02430) says the period of the
// complete pass should be set by how much the cheap signal misses. `diff`
// prints that number so the schedule stops being a guess.

// cmdCatalogGroup dispatches the catalog verbs.
var catalogVerbs = map[string]func([]string) error{
	"ids":    catalogIDs,
	"check":  catalogCheck,
	"sweep":  cmdSync,
	"genres": catalogGenres,
	"new":    catalogNew,
	"diff":   catalogDiff,
	"size":   catalogSize,
	"apps":   catalogApps,
}

func cmdCatalogGroup(args []string) error {
	verbs := catalogVerbs
	if len(args) == 0 {
		return records.Invalid("catalog: need a verb\n\n%s", catalogUsage)
	}
	// A leading flag is a missing verb, not a shorthand for one.
	//
	// This did route to `ids`, because `catalog -shards 0-99` reads naturally
	// and used to work. It is a trap: `ids` with no -shards sweeps all 83,445
	// of them, so any mistyped verb -- or a wrapper that puts a global flag
	// first -- became a four-hour, eighteen-gigabyte run instead of an error
	// message. Nothing about the shorthand is worth that.
	verb := args[0]
	// Asking for help is not a missing verb: `gpscrape -h` tells you to run
	// `<command> -h`, and that has to work for catalog too.
	if verb == "-h" || verb == "-help" || verb == "--help" {
		fmt.Print(catalogUsage)
		return nil
	}
	if strings.HasPrefix(verb, "-") {
		return records.Invalid("catalog: %s is a flag, not a verb; did you mean `catalog ids %s`?\n\n%s",
			verb, strings.Join(args, " "), catalogUsage)
	}
	run, ok := verbs[verb]
	if !ok {
		return records.Invalid("catalog: unknown verb %q\n\n%s", verb, catalogUsage)
	}
	return run(args[1:])
}

const catalogUsage = `Usage: gpscrape catalog <verb> [flags]

  check    has Google republished the catalog?          2 requests
  new      apps the store lists as recently published   17 requests
  size     how many apps there are, give or take 1%     ~900 requests
  genres   genre changes and removals, from a snapshot  ~3.3k requests
  sweep    the complete id list, and the exact count    ~88k requests
  apps     the id list, by genre, from what is on disk  none
  diff     compare two snapshots already on disk        none
  ids      stream ids without keeping any state         as many as you ask for
`

// ---- check ----

type checkRecord struct {
	Generation string  `json:"generation"`
	Built      string  `json:"built,omitempty"`
	AgeHours   int     `json:"age_hours"`
	Shards     int     `json:"shards"`
	Have       string  `json:"have,omitempty"`
	HaveSample float64 `json:"have_sample_pct,omitempty"`
	UpToDate   bool    `json:"up_to_date"`
}

func catalogCheck(args []string) error {
	c := newCommon("catalog check")
	// The same default as every other verb in this group. It used to be "",
	// which made upToDate answer false unconditionally -- and the one thing
	// this command exists for is to stop a consumer running 83,445 requests it
	// does not need. A flag nobody was told to pass should not be what stands
	// between them and that.
	dir := c.fs.String("dir", "catalog", "snapshot directory to compare against")
	if err := c.parse(args); err != nil {
		return err
	}
	if err := c.noArgs("catalog check"); err != nil {
		return err
	}
	ctx, stop := c.context()
	defer stop()

	gen, err := c.client().SitemapGeneration(ctx)
	if err != nil {
		return err
	}

	rec := checkRecord{Generation: gen.ID(), Shards: gen.Shards}
	if built, ok := gen.Built(); ok {
		rec.Built = built.Format(time.RFC3339)
		rec.AgeHours = int(time.Since(built).Hours())
	}
	if *dir != "" {
		if prev, ok := latestManifest(*dir); ok {
			rec.Have = prev.Generation.ID()
			rec.HaveSample = prev.SamplePct
			// A sample of this generation is not this generation. Saying
			// otherwise is how the full sweep never runs.
			rec.UpToDate = prev.Generation.ID() == gen.ID() && prev.complete()
		}
	}
	return emitOne(rec)
}

// ---- genres ----

// genreRecord reports what one lookup found.
//
// Change "gone" means the app is not listed *in the country this run used*,
// which is not the same as removed from the store. Measured on 200 ids the
// catalog pipeline had classified as dead: 198 were gone everywhere, and two
// were not -- one available only in Russia, and one available in Brazil,
// Germany, India, Japan, Kazakhstan, Russia and Turkey, that is everywhere the
// probe looked except the United States it was run from. On 316,400 apparently
// dead ids that rate is a few thousand wrongly buried.
//
// Country is recorded for that reason: a consumer that treats "gone" as
// removal has to know which storefront said so, and `gpscrape availability`
// is what settles it across markets.
type genreRecord struct {
	AppID   string `json:"app_id"`
	GenreID string `json:"genre_id,omitempty"`
	Genre   string `json:"genre,omitempty"`
	Was     string `json:"was,omitempty"`
	Change  string `json:"change"` // first_seen | changed | gone | same
	// Country is where the answer came from: the storefront that supplied the
	// genre, or -- for "gone" -- every storefront that was asked and could not
	// find it.
	Country string `json:"country,omitempty"`
	Error   string `json:"error,omitempty"`
}

func catalogGenres(args []string) error {
	c := newCommon("catalog genres")
	c.adaptiveByDefault()
	dir := c.fs.String("dir", "catalog", "directory holding the snapshot to read")
	from := c.fs.String("ids", "", "read ids from this file instead of the snapshot (- for stdin)")
	confirm := c.fs.String("confirm-gone", "de,in,br,jp,ru",
		"before calling an app gone, look for it in these storefronts too; empty trusts -country alone")
	prune := c.fs.Bool("prune", false,
		"drop table rows for ids the snapshot no longer lists; refused with -ids, "+
			"because a subset cannot tell you what is gone")
	all := c.fs.Bool("all", false, "emit every app, not only the changes")
	if err := c.parse(args); err != nil {
		return err
	}
	if err := c.noArgs("catalog genres"); err != nil {
		return err
	}
	ctx, stop := c.context()
	defer stop()

	// This verb writes the genre table into -dir and reads the snapshot the
	// table describes, so it takes the same lock a sweep does. Without it a
	// nightly genres pass and a weekly sweep that overlap by an hour publish a
	// table derived from one snapshot beside a manifest describing another.
	lock, err := lockDir(*dir)
	if err != nil {
		return err
	}
	defer lock.release()

	ids, closeIDs, readErr, coverage, err := idSource(*dir, *from)
	if err != nil {
		return err
	}
	defer closeIDs()

	// -prune deletes rows for ids this run did not see, which is only sound
	// when the run saw everything. With -ids it would shrink the table to the
	// subset -- the exact destruction the merge was added to prevent.
	if *prune && *from != "" {
		return records.Invalid("-prune needs the whole snapshot: with -ids it would delete every row " +
			"outside the list you passed, which is not the same as the app being gone")
	}
	// A sampled snapshot is the same kind of subset with a manifest saying so.
	if *prune && coverage > 0 {
		return records.Invalid("-prune needs the whole snapshot: the latest one covers %g%% of the catalog, "+
			"and pruning against it would delete every row outside the sample", coverage)
	}

	// A caller's list comes in any order; the merge below needs the
	// snapshot's. The snapshot is sorted already and streams untouched.
	if *from != "" {
		if ids, err = sortedIDs(ctx, ids); err != nil {
			return err
		}
		// The list is read whole here, so a break in it is known before
		// any request goes out.
		if err := readErr(); err != nil {
			return fmt.Errorf("reading the ids stopped partway: %w; the genre table was left untouched", err)
		}
	}

	// What each app's genre was last time, so a change can be named as one,
	// and the new table, both from one walk of the old table alongside the
	// ids: see genres_merge.go. Nothing is held per app except the ids that
	// came back absent.
	//
	// The new table starts from what is already known rather than from
	// nothing. Building it only from what this run saw made two ordinary
	// situations destructive: one transient 503 dropped an app from the
	// table, and running with -ids on a subset shrank the table to that
	// subset. Either way the app comes back as "first_seen" next time, which
	// is a change that did not happen -- and at catalog scale a 0.1% error
	// rate is thousands of them a day. So a failed lookup keeps its row, and
	// a row no id matched is kept unless -prune says otherwise. It is the
	// same reasoning the sweep applies to failed shards: quietly wrong deltas
	// are worse than admitting the pass was incomplete.
	prevPath := genresPath(*dir)
	if err := checkTableSorted(prevPath); err != nil {
		return err
	}

	// A run over the snapshot can resume an interrupted one: see
	// genres_resume.go. A run over -ids never does, and clears any state an
	// earlier snapshot run left, since its merge file is about to be reused.
	cur, err := genresRunIdentity(*dir, c.lang, c.country)
	if err != nil {
		return err
	}
	var res genresResume
	resumed := false
	if *from == "" {
		res, resumed = loadGenresResume(*dir, cur)
	}
	if !resumed {
		dropGenresResume(*dir)
		res = cur
	}

	prev, err := openTable(prevPath)
	if err != nil {
		return err
	}
	defer prev.close()
	var merge *tableMerge
	var absent []absentID
	if resumed {
		if absent, err = absentFromMerge(prevPath+".merge", res.Offset); err != nil {
			return fmt.Errorf("resume the genre pass: %w", err)
		}
		if res.MainDone {
			merge = &tableMerge{prev: prev, path: prevPath + ".merge"}
		} else if merge, err = resumeTableMerge(prev, prevPath+".merge", res.Offset, res.LastID); err != nil {
			return fmt.Errorf("resume the genre pass: %w", err)
		}
		merge.keep = true
		if res.MainDone {
			fmt.Fprintf(os.Stderr, "resuming the genre pass: the walk is done, confirming %d absent ids\n", len(absent))
		} else {
			fmt.Fprintf(os.Stderr, "resuming the genre pass after %s (%d apps done)\n", res.LastID, res.Seen)
		}
		ids = skipThrough(ids, res.LastID)
	} else if merge, err = newTableMerge(prev, prevPath+".merge"); err != nil {
		return err
	}
	defer merge.abandon() // removes the first-pass file unless a checkpoint keeps it

	e := newEmitter(os.Stdout)
	defer func() { _ = e.flush() }()

	seen, changed, failed := res.Seen, res.Changed, res.Failed
	var gone int
	var lastErr error
	if res.LastErr != "" {
		lastErr = errors.New(res.LastErr)
	}
	progress := progressTo(os.Stderr)

	// checkpoint records where the walk is, so an interruption after it
	// resumes here. Records printed so far are flushed with it: a resumed run
	// prints only what comes after, so stdout across the two runs is each id
	// once -- except after a hard kill, which can lose or repeat the records
	// between the last checkpoint and the kill.
	resumable := *from == ""
	lastDone, sinceCheckpoint := res.LastID, 0
	checkpoint := func(mainDone bool) error {
		if !resumable {
			return nil
		}
		var off int64
		if merge.f != nil {
			var err error
			if off, err = merge.checkpoint(); err != nil {
				return err
			}
		} else {
			// Finished and closed: finish flushed and synced it.
			st, err := os.Stat(merge.path)
			if err != nil {
				return err
			}
			off = st.Size()
		}
		if err := e.flush(); err != nil {
			return err
		}
		res.LastID, res.MainDone, res.Offset = lastDone, mainDone, off
		res.Seen, res.Changed, res.Failed = seen, changed, failed
		if lastErr != nil {
			res.LastErr = lastErr.Error()
		}
		if err := saveGenresResume(*dir, res); err != nil {
			return err
		}
		merge.keep, sinceCheckpoint = true, 0
		return nil
	}

	if !res.MainDone {
		for d, err := range c.client().DigestsSeq(ctx, ids, googleplayscraper.DigestOptions{
			Lang: c.lang, Country: c.country, Concurrency: c.concurrency,
			Progress: func(p googleplayscraper.DigestProgress) {
				if progress != nil && p.Requests%10 == 0 {
					_, _ = fmt.Fprintf(progress, "\r%d apps, %d absent, %d requests",
						p.Apps, p.Absent, p.Requests)
				}
			},
		}) {
			if err != nil {
				// Interrupted, or the store stopped answering: keep what is
				// done for the next run.
				if cerr := checkpoint(false); cerr != nil {
					return fmt.Errorf("%w (and the progress could not be saved: %w)", err, cerr)
				}
				if resumable {
					return fmt.Errorf("%w (progress kept: a rerun resumes after %s)", err, lastDone)
				}
				return err
			}
			// An id that failed because this run is being stopped is not a
			// result: the packs in flight come back with the context's error
			// on every id. Counted as a failed lookup it advanced lastDone, so
			// a resume skipped those ids and an app the table did not know
			// yet never got a row -- 15% of a table lost when the stop landed
			// mid-pack. Stop here instead, with lastDone at the last real
			// answer.
			if d.Err != nil && ctx.Err() != nil {
				if cerr := checkpoint(false); cerr != nil {
					return fmt.Errorf("%w (and the progress could not be saved: %w)", ctx.Err(), cerr)
				}
				if resumable {
					return fmt.Errorf("%w (progress kept: a rerun resumes after %s)", ctx.Err(), lastDone)
				}
				return ctx.Err()
			}
			if sinceCheckpoint >= genresCheckpointEvery {
				if err := checkpoint(false); err != nil {
					return err
				}
			}
			seen++
			was, known, err := merge.lookup(d.AppID)
			if err != nil {
				return err
			}
			rec := genreRecord{AppID: d.AppID, GenreID: d.GenreID, Genre: d.Genre, Was: was}

			switch {
			case d.Err != nil:
				// Keep whatever the table already said. A request that failed is
				// not evidence about the app.
				rec.Change, rec.Error = "error", d.Err.Error()
				failed++
				lastErr = d.Err
				if known {
					err = merge.row(d.AppID, was, "")
				}
			case !d.Listed:
				// Held, not buried. "Not listed in this storefront" is not
				// "removed from the store": of 200 ids this pipeline had called
				// dead, two were alive elsewhere -- one only in Russia, one in
				// every market probed except the United States it was run from.
				// An app available in one country is still an app.
				absent = append(absent, absentID{d.AppID, was, known})
				mark := markAbsentFirst
				if known {
					mark = markAbsent
				}
				err = merge.row(d.AppID, was, mark)
			case !known:
				rec.Change = "first_seen"
				err = merge.row(d.AppID, d.GenreID, "")
			case was != d.GenreID:
				rec.Change = "changed"
				changed++
				err = merge.row(d.AppID, d.GenreID, "")
			default:
				rec.Change = "same"
				err = merge.row(d.AppID, d.GenreID, "")
			}
			if err != nil {
				return err
			}
			lastDone = d.AppID
			sinceCheckpoint++
			if !d.Listed && d.Err == nil {
				continue // reported after the confirmation pass
			}

			if *all || rec.Change != "same" {
				if err := e.emit(rec); err != nil {
					return err
				}
			}
		}
		if progress != nil {
			_, _ = fmt.Fprintln(progress)
		}
		if err := readErr(); err != nil {
			// The list ended early. Everything after the break would read as
			// unmatched, and -prune would delete it.
			merge.keep = false
			dropGenresResume(*dir)
			return fmt.Errorf("reading the ids stopped partway: %w; the genre table was left untouched", err)
		}
		if err := merge.finish(); err != nil {
			return err
		}
		merge.f = nil
		// The walk is done; what is left is the confirmation pass, which a
		// resume can run on its own from the absent marks.
		if err := checkpoint(true); err != nil {
			return err
		}
	}

	// Second opinion on everything the first storefront could not see.
	//
	// Cheap, because it only asks about the ids that came back absent, and the
	// digest packs a thousand of them into one request: on the live catalog
	// that is about 310 requests per extra storefront against the 3,400 the
	// main pass costs.
	absentIDs := make([]string, len(absent))
	for i, a := range absent {
		absentIDs[i] = a.id
	}
	stillGone, err := confirmGone(ctx, c, absentIDs, splitCountries(*confirm), progress)
	if err != nil {
		return err
	}
	outcome := make([]settled, len(absent))
	for i, a := range absent {
		rec := genreRecord{AppID: a.id, Was: a.was}
		switch g, alive := stillGone.foundIn[a.id]; {
		case alive:
			// Alive somewhere. Its genre comes from wherever it answered.
			rec.GenreID, rec.Country = g.genre, g.country
			switch {
			case !a.known:
				rec.Change = "first_seen"
			case a.was != g.genre:
				rec.Change, changed = "changed", changed+1
			default:
				rec.Change = "same"
			}
			outcome[i] = settledAlive
		case stillGone.unconfirmed[a.id] != nil:
			// Absent where the storefronts answered, unknown where they did
			// not. The table keeps whatever it said, exactly as the main pass
			// does for a failed lookup, and the id counts as a failure rather
			// than a removal so the all-failed guard below can see it.
			rec.Change, rec.Error = "error", stillGone.unconfirmed[a.id].Error()
			failed++
			lastErr = stillGone.unconfirmed[a.id]
			outcome[i] = settledUnconfirmed
		default:
			// Every storefront asked answered, and none of them has it. The
			// storefronts are named, so "gone" carries its own scope: absent
			// from these, not proven absent from every market Google runs.
			rec.Change = "gone"
			rec.Country = strings.Join(append([]string{c.country}, stillGone.checked...), ",")
			gone++
			outcome[i] = settledGone
		}
		if *all || rec.Change != "same" {
			if err := e.emit(rec); err != nil {
				return err
			}
		}
	}

	// A run in which nothing resolved is not a run that found nothing. Left to
	// save, it wrote an empty table over a good one and exited 0 -- reproduced
	// while rate-limited, where all 1,740 lookups failed and the summary still
	// read "1740 apps". This is the rule the sweep already applies to failed
	// shards, which had not been carried across.
	if failed == seen && seen > 0 {
		// Nothing to resume either: a rerun should ask again.
		merge.keep = false
		dropGenresResume(*dir)
		return fmt.Errorf("all %d lookups failed, so the genre table was left untouched; "+
			"the last error was: %v", seen, lastErr)
	}

	// The genre table is the useful by-product: a snapshot minus the ids that
	// no longer resolve, which is what the catalog actually is. Nothing else
	// publishes it.
	//
	// -prune drops rows for ids that left the sitemap without ever being seen
	// to go. The merge that stops a transient error deleting an app also means
	// nothing else ever removes these, so the table would grow monotonically
	// away from the catalog it describes.
	settleOf := func(i int) (settled, string) {
		return outcome[i], stillGone.foundIn[absent[i].id].genre
	}
	// The old table is closed before the new one is renamed over it: Windows
	// refuses to rename onto a file a process holds open. The walk finished
	// reading it at merge.finish.
	prev.close()
	pruned, err := settleTable(merge.path, prevPath, absentIDs, settleOf, *prune && failed < seen)
	if err != nil {
		return err
	}
	// Published: the walk is finished and nothing is left to resume.
	merge.keep = false
	_ = os.Remove(genresResumePath(*dir))
	if coverage > 0 {
		// The table now merges rather than overwrites, so reading a sample is
		// harmless -- but the counts above describe the sample, not the store,
		// and nothing else in the output says so.
		fmt.Fprintf(os.Stderr, "note: the snapshot covers %g%% of the catalog; "+
			"these counts are of that sample, not the store\n", coverage)
	}
	// failed is reported rather than folded into the total: "1740 apps" reads
	// as success, and a run where a tenth of them errored is not one.
	summary := fmt.Sprintf("%d apps, %d changed, %d gone", seen, changed, gone)
	if failed > 0 {
		summary += fmt.Sprintf(", %d could not be read", failed)
	}
	if pruned > 0 {
		summary += fmt.Sprintf(", %d pruned", pruned)
	}
	fmt.Fprintf(os.Stderr, "%s -> %s\n", summary, prevPath)
	return e.flush()
}

// idSource yields ids from a file, from stdin, or from the newest snapshot.
// idSource opens the list of app ids to work through, and reports what share
// of the catalog it covers: 0 for a complete snapshot or a caller-supplied
// list, the sampled percentage otherwise. Callers report that, because a
// sample and the catalog are the same sorted list of ids from the outside and
// only the manifest knows the difference.
//
// readErr reports what stopped the list early, once it has been read. It has
// to be asked: a sequence cannot return an error, and a snapshot cut short by
// a corrupt gzip stream or an overlong line otherwise reads as a shorter
// catalog -- and with -prune the genre pass deleted every row past the break.
func idSource(dir, from string) (ids iter.Seq[string], closeIDs func(), readErr func() error, coverage float64, err error) {
	switch {
	case from == "-":
		seq, rerr := scanIDs(os.Stdin)
		return seq, func() {}, rerr, 0, nil
	case from != "":
		f, err := os.Open(from)
		if err != nil {
			return nil, nil, nil, 0, err
		}
		seq, rerr := scanIDs(f)
		return seq, func() { _ = f.Close() }, rerr, 0, nil
	}

	m, ok := latestManifest(dir)
	if !ok {
		return nil, nil, nil, 0, records.Errorf(records.CodeNotFound, "no snapshot in %s; run `gpscrape catalog sweep -dir %s` first, "+
			"or pass -ids", dir, dir)
	}
	path := filepath.Join(dir, m.File)
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, nil, 0, err
	}
	zr, err := gzip.NewReader(f)
	if err != nil {
		_ = f.Close()
		return nil, nil, nil, 0, err
	}
	seq, rerr := scanIDs(zr)
	return seq, func() { _ = zr.Close(); _ = f.Close() }, rerr, m.SamplePct, nil
}

// scanIDs streams lines, and returns a function reporting the error that
// ended them, if any. The point of a sequence rather than a slice is that a
// catalog snapshot is 3.4 million ids, and the digest pass holds one pack per
// worker rather than all of them.
func scanIDs(r interface{ Read([]byte) (int, error) }) (iter.Seq[string], func() error) {
	var scanErr error
	seq := func(yield func(string) bool) {
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" {
				continue
			}
			if !yield(line) {
				return
			}
		}
		scanErr = sc.Err()
	}
	return seq, func() error { return scanErr }
}

func genresPath(dir string) string { return filepath.Join(dir, "genres.tsv") }

// streamGenres walks the genre table a line at a time, calling fn for each
// entry, and returns the path it read and how many rows it saw.
//
// Reading the app list filters and prints, so it needs no map: doing it
// through one cost 422MB of resident memory on a real 3.2M-row table to
// produce 9MB of ids -- and the table only grows, so the cost grows with it.
// The genre pass needs no map either; it walks the table alongside the
// snapshot (genres_merge.go).
//
// Rows arrive in the order the genre pass wrote them, which is sorted by app id.
// Callers that need sorted output still sort, because a hand-edited file is
// not a contract, but sorting what survives the filter is a different size of
// problem from holding everything.
func streamGenres(dir string, fn func(appID, genreID string)) (string, int, error) {
	path := genresPath(dir)
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return path, 0, nil
		}
		return path, 0, err
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	var rows int
	for sc.Scan() {
		id, genre, ok := strings.Cut(sc.Text(), "\t")
		if !ok {
			continue
		}
		rows++
		fn(id, genre)
	}
	return path, rows, sc.Err()
}

// writeTableAtomic writes a table through fill: a temporary file, fsync,
// rename, then the directory synced, so a reader sees the old table or the
// new one and never half of either.
func writeTableAtomic(path string, fill func(*bufio.Writer) error) error {
	tmp := path + ".tmp"
	f, err := openWrite(tmp, createFlags, 0o666)
	if err != nil {
		return err
	}
	// Each error path removes the temporary file. Leaving it behind was
	// harmless only by luck: the next run truncates it, and until then a
	// half-written table sat beside the good one under a name that says which
	// it is.
	fail := func(err error) error {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	w := bufio.NewWriterSize(f, 1<<20)
	if err := fill(w); err != nil {
		return fail(err)
	}
	if err := w.Flush(); err != nil {
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := stillOurs(f, tmp); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := renameFile(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	// The rename is what a reader sees, and a rename lives in the page cache
	// like anything else until the directory entry is durable.
	if err := syncDir(filepath.Dir(path)); err != nil {
		return fmt.Errorf("publish %s: %w", path, err)
	}
	return nil
}

// ---- new ----

type newRecord struct {
	AppID    string `json:"app_id"`
	Category string `json:"category"`
	Rank     int    `json:"rank"`
	Seen     string `json:"seen"` // first | known
	At       string `json:"at"`
}

func catalogNew(args []string) error {
	c := newCommon("catalog new")
	dir := c.fs.String("dir", "catalog", "directory holding the signal log")
	cats := c.fs.String("categories", "game", `"game", "all", or a comma-separated list`)
	collection := c.fs.String("collection", "new_free", "one of: "+collectionNames())
	num := c.fs.Int("num", 200, "apps to ask for per category")
	if err := c.parse(args); err != nil {
		return err
	}
	if err := c.noArgs("catalog new"); err != nil {
		return err
	}
	col, ok := collections[strings.ToLower(*collection)]
	if !ok {
		return records.Invalid("unknown collection %q; want one of: %s", *collection, collectionNames())
	}
	categories, err := categoryList(*cats)
	if err != nil {
		return err
	}

	ctx, stop := c.context()
	defer stop()

	// Append-only, one line per observation. The first time an id is seen is
	// the datum: a file holding "the current signalled set" would destroy the
	// history that measuring the signal's recall depends on, and that loss is
	// unrecoverable after the fact.
	logPath := filepath.Join(*dir, "signal.log")
	// No window here: `new` asks "have I ever seen this id", which is the
	// whole log by definition.
	known, err := loadSignalIDs(logPath, time.Time{}, time.Time{})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(*dir, 0o755); err != nil {
		return err
	}
	lf, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = lf.Close() }()
	lw := bufio.NewWriter(lf)
	// Registered after the file's own Close, so it runs before it. An emit or
	// encode failure below returns without reaching the explicit Flush, and
	// what the buffer still holds is observations already made. The explicit
	// Flush stays as the one whose error is reported.
	defer func() { _ = lw.Flush() }()
	lenc := json.NewEncoder(lw)

	e := newEmitter(os.Stdout)
	defer func() { _ = e.flush() }()

	now := time.Now().UTC().Format(time.RFC3339)
	var total, fresh, failedCats int
	var lastErr error
	for _, cat := range categories {
		results, err := c.client().List(ctx, googleplayscraper.ListOptions{
			Collection: col, Category: cat, Num: *num,
			Lang: c.lang, Country: c.country,
		})
		if err != nil {
			failedCats++
			lastErr = err
			fmt.Fprintf(os.Stderr, "%s: %v\n", cat, err)
			continue
		}
		for rank, r := range results {
			total++
			rec := newRecord{AppID: r.AppID, Category: string(cat), Rank: rank + 1, At: now, Seen: "known"}
			if !known[r.AppID] {
				known[r.AppID] = true
				rec.Seen = "first"
				fresh++
			}
			if err := lenc.Encode(rec); err != nil {
				return err
			}
			if rec.Seen == "first" {
				if err := e.emit(rec); err != nil {
					return err
				}
			}
		}
	}
	if err := lw.Flush(); err != nil {
		return err
	}
	// One failed category out of seventeen is an ordinary day. Every category
	// failing is a run that observed nothing, and it used to be
	// indistinguishable from "the store published nothing new": exit 0, empty
	// stdout, and a signal.log that grew a zero-line entry.
	if failedCats == len(categories) {
		return fmt.Errorf("all %d categories failed, so nothing was observed; the last error was: %w",
			failedCats, lastErr)
	}
	fmt.Fprintf(os.Stderr, "%d observations across %d categories, %d not seen before -> %s\n",
		total, len(categories), fresh, logPath)
	return e.flush()
}

func categoryList(spec string) ([]googleplayscraper.Category, error) {
	switch strings.ToLower(spec) {
	case "game", "games":
		var out []googleplayscraper.Category
		for _, c := range googleplayscraper.GameCategories() {
			// The parent GAME category duplicates its children's listings.
			if c != googleplayscraper.CategoryGame {
				out = append(out, c)
			}
		}
		return out, nil
	case "all":
		return googleplayscraper.AllCategories, nil
	}
	// Checked against the known list rather than passed through. An unknown
	// category returned no observations, wrote an empty signal log and exited
	// 0 -- indistinguishable from "nothing new was published" -- while
	// -collection, one flag along, has always named its valid values.
	known := make(map[googleplayscraper.Category]struct{}, len(googleplayscraper.AllCategories))
	for _, c := range googleplayscraper.AllCategories {
		known[c] = struct{}{}
	}

	var out []googleplayscraper.Category
	for _, s := range strings.Split(spec, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		cat := googleplayscraper.Category(strings.ToUpper(s))
		if _, ok := known[cat]; !ok {
			return nil, fmt.Errorf("unknown category %q; `gpscrape categories` lists all %d of them, "+
				"or use \"game\" or \"all\"", s, len(googleplayscraper.AllCategories))
		}
		out = append(out, cat)
	}
	if len(out) == 0 {
		return nil, records.Invalid("no categories in %q", spec)
	}
	return out, nil
}

// loadSignalIDs reads the ids `new` has observed, optionally restricted to a
// time window.
//
// The window is what makes the precision figure mean anything. It used to load
// the whole log and divide the matches of one generation's window by every id
// ever observed, so the number fell as the log aged whatever the signal was
// doing -- a ratio with its numerator and denominator taken from different
// periods. Each record carries the time it was seen; now it is used.
func loadSignalIDs(path string, since, until time.Time) (map[string]bool, error) {
	out := map[string]bool{}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		var rec newRecord
		if json.Unmarshal(sc.Bytes(), &rec) != nil || rec.AppID == "" {
			continue
		}
		if !since.IsZero() || !until.IsZero() {
			at, err := time.Parse(time.RFC3339, rec.At)
			if err != nil {
				// A record with no usable timestamp cannot be placed in the
				// window, and counting it would put the denominator back where
				// it was. Skipped rather than assumed.
				continue
			}
			if !since.IsZero() && at.Before(since) {
				continue
			}
			if !until.IsZero() && at.After(until) {
				continue
			}
		}
		out[rec.AppID] = true
	}
	return out, sc.Err()
}

// ---- diff ----

// diffRecord accounts for what changed between two snapshots, and for how much
// of it the cheap signal had already reported.
//
// The recall figure is the reason this command exists. A pipeline that runs
// `new` daily and `sweep` occasionally has to choose how occasionally, and the
// answer depends on how many additions the ranked lists never mention. That is
// measurable only by comparing what `new` said against what a complete sweep
// found, so the accounting is printed unasked rather than hidden behind a flag:
// a metric behind a flag is a metric nobody has.
//
// unsignalledPerDay is the number to schedule on. Sweep when the additions the
// signal cannot see have piled up beyond what you are willing to be wrong by.
// changeRecord is one id that appeared or disappeared between two snapshots.
//
// The summary alone -- "3 added, 2 removed" -- is not something a database can
// act on: keeping a local copy current means knowing *which* ids moved. The
// sweep has always written them to delta-<from>-to-<to>.json, but reaching that
// meant computing a filename inside a directory whose layout this tool calls
// its own business, so the ids existed and were not reachable.
type changeRecord struct {
	AppID  string `json:"app_id"`
	Change string `json:"change"` // "added" or "removed"
	From   string `json:"from"`
	To     string `json:"to"`
}

type diffRecord struct {
	From    string       `json:"from"`
	To      string       `json:"to"`
	Added   int          `json:"added"`
	Removed int          `json:"removed"`
	Signal  *signalStats `json:"signal,omitempty"`
}

type signalStats struct {
	// Observed counts only what was seen inside the window the two snapshots
	// span, so precision is a ratio of two figures from one period.
	Observed          int     `json:"observed"`
	MatchedAdded      int     `json:"matched_added"`
	Recall            float64 `json:"recall"`
	Precision         float64 `json:"precision"`
	UnsignalledAdded  int     `json:"unsignalled_added"`
	UnsignalledPerDay float64 `json:"unsignalled_per_day,omitempty"`
	DaysBetween       float64 `json:"days_between,omitempty"`
}

func catalogDiff(args []string) error {
	c := newLocalCommon("catalog diff")
	changes := c.fs.Bool("changes", false,
		"emit one record per changed id instead of the summary: what a refresh consumes")
	dir := c.fs.String("dir", "catalog", "directory holding the snapshots and signal log")
	if err := c.parse(args); err != nil {
		return err
	}

	var fromPath, toPath string
	switch len(c.args) {
	case 0:
		var err error
		fromPath, toPath, err = twoNewestSnapshots(*dir)
		if err != nil {
			return err
		}
	case 2:
		fromPath, toPath = c.args[0], c.args[1]
	default:
		return records.Invalid("catalog diff: give two snapshot files, or none to use the two newest in -dir")
	}

	// A sampled snapshot looks exactly like a complete one -- a sorted list of
	// ids -- so nothing about the files themselves stops this comparison. The
	// manifests know the difference, and everything below is about asking them
	// before answering, because every way of getting this wrong produces a
	// number that looks like a real catalog event.
	fromGen, fromOK := generationOfSnapshot(fromPath)
	toGen, toOK := generationOfSnapshot(toPath)
	if !fromOK || !toOK {
		// Without a generation there is no manifest to consult, so coverage
		// cannot be established -- and an unchecked diff is exactly the one
		// that reports the sampling as a catalog change. This used to continue
		// with a zero Generation, whose ID() is "_".
		bad := fromPath
		if fromOK {
			bad = toPath
		}
		return fmt.Errorf("cannot tell which generation %s came from, so its coverage cannot be checked; "+
			"snapshots are named snapshot-<date>_<run>.txt.gz and diffing is only meaningful between two "+
			"whose manifests agree", bad)
	}

	// A manifest, not just a name that parses. sampleOf returns 0 for a
	// missing manifest and 0 means "complete", so a sampled snapshot renamed
	// to a well-formed generation id read as a full sweep -- and diffing it
	// against a real one reported the whole catalog as removed, exit 0. The
	// error text already claimed to be about coverage; now the check is.
	for _, g := range []googleplayscraper.Generation{fromGen, toGen} {
		if _, err := os.Stat(filepath.Join(*dir, "manifest-"+g.ID()+".json")); err != nil {
			return fmt.Errorf("no manifest for %s in %s, so its coverage cannot be checked; "+
				"diffing is only meaningful between snapshots whose manifests agree", g.ID(), *dir)
		}
	}

	fromSamp := sampleOf(*dir, fromGen)
	toSamp := sampleOf(*dir, toGen)
	if fromSamp != toSamp {
		return fmt.Errorf(
			"refusing to diff snapshots swept differently: %s covered %s, %s covered %s. "+
				"The difference would be the sampling, not the catalog",
			fromGen.ID(), coverageLabel(fromSamp), toGen.ID(), coverageLabel(toSamp))
	}

	// Equal coverage is not enough once either side is a sample.
	//
	// The sample seed is derived from the generation id, deliberately: shard
	// indices name different shards in different builds, so carrying a seed
	// across a rebuild would draw a set that has stopped meaning anything.
	// The consequence is that two 1% samples of two generations cover almost
	// disjoint sets of apps *by construction* -- and diffing them reported
	// every id in each as added and removed respectively. Equal percentages,
	// nothing in common, and a number that reads as a catastrophic but
	// plausible catalog event.
	if fromSamp > 0 && fromGen.ID() != toGen.ID() {
		return fmt.Errorf(
			"refusing to diff samples from two generations (%s and %s, both %.2f%%): "+
				"each build samples its own shards, so the two cover different apps and the "+
				"difference would be the sampling, not the catalog",
			fromGen.ID(), toGen.ID(), fromSamp)
	}

	d, err := diffSnapshots(fromGen, toGen, fromPath, toPath)
	if err != nil {
		return err
	}

	// One record per changed id, which is what a daily refresh consumes. The
	// summary stays the default because it is the cheap answer to "did
	// anything happen", and on a generation roll this stream is millions of
	// lines.
	if *changes {
		e := newEmitter(os.Stdout)
		defer func() { _ = e.flush() }()
		for _, id := range d.Added {
			if err := e.emit(changeRecord{id, "added", fromGen.ID(), toGen.ID()}); err != nil {
				return err
			}
		}
		for _, id := range d.Removed {
			if err := e.emit(changeRecord{id, "removed", fromGen.ID(), toGen.ID()}); err != nil {
				return err
			}
		}
		if w := progressTo(os.Stderr); w != nil {
			_, _ = fmt.Fprintf(w, "%d added, %d removed between %s and %s\n",
				len(d.Added), len(d.Removed), fromGen.ID(), toGen.ID())
		}
		return e.flush()
	}

	rec := diffRecord{
		From:    fromGen.ID(),
		To:      toGen.ID(),
		Added:   len(d.Added),
		Removed: len(d.Removed),
	}
	rec.Signal = signalAccounting(*dir, d.Added, fromGen, toGen)
	return emitOne(rec)
}

// signalAccounting measures how much of what the sweep found had already been
// reported by `new`. It returns nil when there is no signal log to measure
// against, rather than reporting a recall of zero, which would read as "the
// signal is useless" instead of "nobody asked it".
func signalAccounting(dir string, added []string, from, to googleplayscraper.Generation) *signalStats {
	// The same window the added set comes from, so precision is a ratio of two
	// things measured over one period.
	since, _ := from.Built()
	until, _ := to.Built()
	observed, err := loadSignalIDs(filepath.Join(dir, "signal.log"), since, until)
	if err != nil || len(observed) == 0 {
		return nil
	}

	inAdded := make(map[string]bool, len(added))
	for _, id := range added {
		inAdded[id] = true
	}
	matched := 0
	for id := range observed {
		if inAdded[id] {
			matched++
		}
	}

	st := &signalStats{
		Observed:         len(observed),
		MatchedAdded:     matched,
		UnsignalledAdded: len(added) - matched,
	}
	if len(added) > 0 {
		st.Recall = float64(matched) / float64(len(added))
	}
	if len(observed) > 0 {
		st.Precision = float64(matched) / float64(len(observed))
	}
	if a, aok := from.Built(); aok {
		if b, bok := to.Built(); bok && b.After(a) {
			days := b.Sub(a).Hours() / 24
			st.DaysBetween = days
			if days > 0 {
				st.UnsignalledPerDay = float64(st.UnsignalledAdded) / days
			}
		}
	}
	return st
}

// twoNewestSnapshots finds the two most recent snapshots in a directory by the
// generations their manifests name.
func twoNewestSnapshots(dir string) (string, string, error) {
	entries, err := filepath.Glob(filepath.Join(dir, "manifest-*.json"))
	if err != nil || len(entries) < 2 {
		return "", "", records.Errorf(records.CodeNotFound, "need two snapshots in %s; found %d", dir, len(entries))
	}
	type snap struct {
		gen  googleplayscraper.Generation
		file string
	}
	var snaps []snap
	for _, path := range entries {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		var m manifest
		derr := json.NewDecoder(f).Decode(&m)
		_ = f.Close()
		if derr != nil || m.File == "" {
			continue
		}
		snaps = append(snaps, snap{m.Generation, filepath.Join(dir, m.File)})
	}
	if len(snaps) < 2 {
		return "", "", records.Errorf(records.CodeNotFound, "need two readable manifests in %s; found %d", dir, len(snaps))
	}
	sort.Slice(snaps, func(i, j int) bool { return snaps[i].gen.Compare(snaps[j].gen) < 0 })
	return snaps[len(snaps)-2].file, snaps[len(snaps)-1].file, nil
}

// generationOfSnapshot reads the generation a snapshot belongs to from its
// filename, which is where writeSnapshot puts it.
func generationOfSnapshot(path string) (googleplayscraper.Generation, bool) {
	base := filepath.Base(path)
	base = strings.TrimPrefix(base, "snapshot-")
	base = strings.TrimSuffix(base, ".txt.gz")
	date, run, ok := strings.Cut(base, "_")
	if !ok {
		return googleplayscraper.Generation{}, false
	}
	return googleplayscraper.Generation{Date: date, Run: run}, true
}

// sampleOf reports what percentage of the catalog a snapshot covered, from its
// manifest. Zero means a complete sweep, which is also what an unreadable
// manifest reports -- the field is absent from every snapshot written before
// sampling existed, and those were all complete.
func sampleOf(dir string, gen googleplayscraper.Generation) float64 {
	f, err := os.Open(filepath.Join(dir, "manifest-"+gen.ID()+".json"))
	if err != nil {
		return 0
	}
	defer func() { _ = f.Close() }()
	var m manifest
	if json.NewDecoder(f).Decode(&m) != nil {
		return 0
	}
	return m.SamplePct
}

func coverageLabel(pct float64) string {
	if pct <= 0 {
		return "the whole catalog"
	}
	return fmt.Sprintf("%.2f%% of it", pct)
}

// ---- size ----
//
// How many apps does the store list? There are two answers and they cost four
// orders of magnitude apart, so -- as everywhere else in this group -- they
// are separate commands rather than a flag:
//
//	size     an estimate that states its own error   ~900 requests, 90s
//	sweep    the count itself, and the ids with it   83,445 requests, ~4.6h
//
// `size` does not have an -exact flag, and the reason is that it would be
// strictly worse than the command that already exists. Exactness costs a full
// pass over every shard however it is spelled, and a full pass that keeps the
// ids costs the same as one that throws them away -- so paying four and a half
// hours to be left holding a single integer is never the right trade. `sweep`
// already reports the count, in the `ids` field of its manifest.
//
// The flag would also be the kind of mistake this group has made before: one
// character between a ninety-second command and a four-hour, eighteen-gigabyte
// one, with nothing in between to catch it.

type sizeRecord struct {
	Generation   string  `json:"generation"`
	Apps         int     `json:"apps"`
	HalfWidth    int     `json:"half_width"`
	RelativePct  float64 `json:"relative_pct"`
	ShardsRead   int     `json:"shards_read"`
	ShardsTotal  int     `json:"shards_total"`
	MeanPerShard float64 `json:"mean_per_shard"`
	// Not omitempty: dispersionZ of exactly 0 is a perfectly uniform hash,
	// which is the healthiest reading there is, and dropping it from the
	// output makes the best case look like a missing field.
	Dispersion  float64 `json:"dispersion"`
	DispersionZ float64 `json:"dispersion_z"`
	TargetPct   float64 `json:"target_pct"`
	MetTarget   bool    `json:"met_target"`
	HashUniform bool    `json:"hash_uniform"`
	// SnapshotIDs is the id count of the complete snapshot of this
	// generation in -dir, when there is one, and SnapshotAgrees whether it
	// lies within the estimate's reach (three standard errors, or exactly
	// when every shard was read). A snapshot short of the estimate is the
	// one sign of ids lost without an error: a shard whose document parsed
	// but held fewer apps than it should.
	SnapshotIDs    int    `json:"snapshot_ids,omitempty"`
	SnapshotAgrees *bool  `json:"snapshot_agrees,omitempty"`
	Warning        string `json:"warning,omitempty"`
}

// snapshotAgrees reports whether a snapshot's id count is what the estimate
// allows: exactly the count when every shard was read, otherwise within
// three standard errors (HalfWidth is the 95% half-width, so 1.96 of them).
// Shards share no ids, so the two count the same thing.
func snapshotAgrees(ids int, size googleplayscraper.CatalogSize) bool {
	if size.Exact {
		return ids == size.Apps
	}
	sigma := size.HalfWidth / 1.96
	return math.Abs(float64(ids-size.Apps)) <= 3*sigma
}

func catalogSize(args []string) error {
	// -exact is the flag someone will reach for, and "flag provided but not
	// defined" would leave them looking for a spelling rather than the command
	// that does the thing.
	for _, a := range args {
		if a == "-exact" || a == "--exact" {
			return fmt.Errorf("catalog size estimates; for the exact count run `gpscrape catalog sweep`, " +
				"which makes the same pass and keeps the ids rather than discarding them")
		}
	}

	c := newCommon("catalog size")
	precision := c.fs.String("precision", "1%", "target relative half-width, e.g. 1% or 0.5% (for the exact count, use `catalog sweep`)")
	pilot := c.fs.Int("pilot", 200, "shards to read before sizing the sample")
	seed := c.fs.Uint64("seed", 0, "fix which shards are drawn (0 derives one from the generation)")
	dir := c.fs.String("dir", "catalog", "check the estimate against this directory's complete snapshot of the same generation, if it has one (\"\" to skip)")
	if err := c.parse(args); err != nil {
		return err
	}

	if err := c.noArgs("catalog size"); err != nil {
		return err
	}

	p, err := parsePercent(*precision)
	if err != nil {
		return records.Invalid("-precision: %w", err)
	}
	opts := googleplayscraper.SizeOptions{
		Precision:   p,
		Pilot:       *pilot,
		Seed:        *seed,
		Concurrency: c.concurrency,
	}

	ctx, stop := c.context()
	defer stop()

	if w := progressTo(os.Stderr); w != nil {
		last := -1
		opts.Progress = func(p googleplayscraper.SizeProgress) {
			// One line per percent, or per 100 shards when the total is the
			// whole catalog and a percent is 834 of them.
			step := max(p.Total/100, 1)
			if p.ShardsRead/step == last {
				return
			}
			last = p.ShardsRead / step
			_, _ = fmt.Fprintf(w, "%s: %d shards, %d apps\n", p.Stage, p.ShardsRead, p.Apps)
		}
	}

	size, err := c.client().CatalogSize(ctx, opts)
	if err != nil {
		return err
	}

	rec := sizeRecord{
		Generation: size.Generation.ID(), Apps: size.Apps,
		HalfWidth: int(size.HalfWidth + 0.5), RelativePct: size.RelativeHalfWidth() * 100,
		ShardsRead: size.ShardsRead, ShardsTotal: size.ShardsTotal,
		MeanPerShard: size.MeanPerShard,
		Dispersion:   size.Dispersion, DispersionZ: size.DispersionZ,
		TargetPct: size.Target * 100, MetTarget: size.MetTarget(),
		HashUniform: size.HashLooksUniform(),
	}
	if !size.MetTarget() {
		// Not a failure, and not silent either. The sample size is solved from
		// the pilot's spread, and a pilot that understates it stops the run
		// short of its own target -- an ordinary outcome, and worth saying,
		// because the number a caller reasons about is the one achieved rather
		// than the one asked for.
		rec.Warning = fmt.Sprintf("asked for %.3g%% and achieved %.3g%%: the pilot underestimated "+
			"the spread between shards; raise -pilot for a tighter first guess",
			size.Target*100, size.RelativeHalfWidth()*100)
	}
	if !size.HashLooksUniform() {
		// The estimate rests on shards being interchangeable. When the spread
		// says they are not, the interval is not merely wide, it is wrong, and
		// saying so is the only useful thing to do.
		rec.Warning = "between-shard spread is not what a uniform hash produces; the shards " +
			"may be organised, in which case a sample of them is biased and only `catalog sweep` is sound"
	}
	if *dir != "" {
		if m, ok := latestManifest(*dir); ok && m.complete() && m.Generation.ID() == size.Generation.ID() {
			agrees := snapshotAgrees(m.IDs, size)
			rec.SnapshotIDs, rec.SnapshotAgrees = m.IDs, &agrees
			if !agrees {
				rec.Warning = strings.TrimPrefix(rec.Warning+"; ", "; ") + fmt.Sprintf(
					"the snapshot of this generation holds %d ids against an estimate of %d +/- %.0f: "+
						"ids may have been lost without an error (a shard that parsed short); sweep it again",
					m.IDs, size.Apps, size.HalfWidth)
			}
		}
	}
	return emitOne(rec)
}

// parsePercent reads "1%", "1", "0.5%" or "0.005" as a fraction. Both spellings
// are common in the wild and guessing wrong by a factor of 100 would silently
// turn a ninety-second run into half a sweep.
func parsePercent(s string) (float64, error) {
	s = strings.TrimSpace(s)
	pct := strings.HasSuffix(s, "%")
	v, err := strconv.ParseFloat(strings.TrimSuffix(s, "%"), 64)
	if err != nil {
		return 0, records.Invalid("%q is not a number", s)
	}
	if pct {
		v /= 100
	} else if v >= 1 {
		// A bare number of 1 or more can only have meant percent: a relative
		// half-width of 100% is not a target anyone sets.
		v /= 100
	}
	// NaN fails every comparison, so a range check written as < and > lets it
	// through: -precision NaN produced a real-looking measurement at a target
	// nobody can name.
	if math.IsNaN(v) || v <= 0 || v >= 1 {
		return 0, records.Invalid("%q is out of range; want something like 1%% or 0.5%%", s)
	}
	return v, nil
}

// ---- apps ----
//
// The genre table is what the other verbs have been building all along, and
// until now nothing could read it back. That made the one thing the catalog is
// actually kept for -- "give me every game in the store" -- a job for a shell
// script over a file whose format is this tool's private business, and it left
// the table with no reader inside the tool at all, which is how it came to be
// so easy to destroy by accident.
//
// The pipeline is: sweep for the ids, genres to resolve them, apps to read the
// answer. Only this last step is free, and it is the one a consumer runs.

type appRecord struct {
	AppID   string `json:"app_id"`
	GenreID string `json:"genre_id"`
}

func catalogApps(args []string) error {
	c := newLocalCommon("catalog apps")
	dir := c.fs.String("dir", "catalog", "directory holding the genre table")
	genre := c.fs.String("genre", "", `keep only this genre; "GAME_*" matches every game category, `+
		`"GAME_PUZZLE" one of them, empty keeps everything`)
	idsOnly := c.fs.Bool("ids-only", false, "print bare ids instead of JSON records")
	allowSample := c.fs.Bool("allow-sample", false,
		"read a table built from a sampled snapshot: a share of the catalog, not the app list")
	if err := c.parse(args); err != nil {
		return err
	}
	if err := c.noArgs("catalog apps"); err != nil {
		return err
	}

	match, err := genreMatcher(*genre)
	if err != nil {
		return err
	}

	// The fifth place that has to know a sampled snapshot from a complete one.
	//
	// Four were fixed together and this verb, added in the very next commit,
	// arrived without the check -- so `catalog apps -genre GAME_*`, which the
	// README offers as "the game index", would hand back 0.001% of the store
	// with exit 0 and an empty stderr. The guard belongs with every reader of
	// this directory, not with the four that happened to exist when it was
	// written; TestEveryCatalogReaderChecksCoverage now walks them.
	if m, ok := latestManifest(*dir); ok && !m.complete() {
		if !*allowSample {
			return fmt.Errorf("the snapshot in %s covers %g%% of the catalog, so this table is a sample "+
				"of it and not the app list; re-run `gpscrape catalog sweep -dir %s` in full and then "+
				"`gpscrape catalog genres -dir %s`, or pass -allow-sample to read the sample as a sample",
				*dir, m.SamplePct, *dir, *dir)
		}
		// Asked for deliberately, so it proceeds -- but says so on every run,
		// and not behind a terminal check: the caller who needs to know a list
		// is 0.05% of the store is the script consuming it, which never has
		// a terminal.
		_, _ = fmt.Fprintf(os.Stderr,
			"note: this is a %g%% sample of the catalog, not its app list\n", m.SamplePct)
	}

	// Only the matches are kept, not the table. On the live catalog that is
	// 347,340 rows held instead of 3,209,964.
	type entry struct{ id, genre string }
	var hits []entry
	path, rows, err := streamGenres(*dir, func(id, genre string) {
		if match(genre) {
			hits = append(hits, entry{id, genre})
		}
	})
	if err != nil {
		return err
	}
	if rows == 0 {
		return records.Errorf(records.CodeNotFound, "no genre table in %s; run `gpscrape catalog sweep -dir %s` then "+
			"`gpscrape catalog genres -dir %s` to build one", *dir, *dir, *dir)
	}

	// Sorted, because this output is compared against yesterday's by whoever
	// consumes it and a differently-ordered file reads as wholly changed. The
	// rows arrive sorted already -- the genre pass writes them that way -- but a
	// file on disk is not a contract, and sorting what survived the filter is
	// cheap next to what was skipped.
	slices.SortFunc(hits, func(a, b entry) int { return strings.Compare(a.id, b.id) })
	// A pattern that matches nothing is almost always a typo -- lower case, a
	// plural, a genre that does not exist -- and the shape it used to take was
	// an empty file and exit 0, which a pipeline cannot tell from "the store
	// has no games". The count line that would have hinted at it goes to
	// stderr behind a terminal check, so the caller this output is written for
	// never saw it.
	if len(hits) == 0 && *genre != "" {
		return records.Errorf(records.CodeNotFound, "no app in %s has a genre matching %q; "+
			"genre ids look like GAME_PUZZLE or PRODUCTIVITY, and `gpscrape categories` lists them",
			path, *genre)
	}
	e := newEmitter(os.Stdout)
	defer func() { _ = e.flush() }()
	for _, h := range hits {
		if *idsOnly {
			if err := e.raw(h.id); err != nil {
				return err
			}
			continue
		}
		if err := e.emit(appRecord{AppID: h.id, GenreID: h.genre}); err != nil {
			return err
		}
	}
	if w := progressTo(os.Stderr); w != nil {
		_, _ = fmt.Fprintf(w, "%d of %d apps in %s\n", len(hits), rows, path)
	}
	return e.flush()
}

// genreMatcher turns the -genre argument into a predicate.
//
// Only a trailing * is supported, and deliberately: the useful case is the
// whole GAME_ family, which is seventeen separate category ids rather than one
// value, and a general glob would invite patterns whose meaning depends on
// what Google happens to have named things this week.
func genreMatcher(pattern string) (func(string) bool, error) {
	switch {
	case pattern == "":
		return func(string) bool { return true }, nil
	case strings.HasSuffix(pattern, "*"):
		prefix := strings.TrimSuffix(pattern, "*")
		if strings.Contains(prefix, "*") {
			return nil, records.Invalid("-genre %q: only a trailing * is supported", pattern)
		}
		return func(g string) bool { return strings.HasPrefix(g, prefix) }, nil
	default:
		if strings.Contains(pattern, "*") {
			return nil, records.Invalid("-genre %q: only a trailing * is supported", pattern)
		}
		return func(g string) bool { return g == pattern }, nil
	}
}

// aliveElsewhere is where an app answered when the primary storefront could
// not see it.
type aliveElsewhere struct{ genre, country string }

type confirmResult struct {
	foundIn map[string]aliveElsewhere
	// unconfirmed holds ids that were never found and whose lookup failed in
	// at least one storefront. A request that failed is not evidence about the
	// app: calling those gone deletes a live row on the strength of a 503.
	unconfirmed map[string]error
	// checked is the storefronts actually asked, in the order they were asked,
	// excluding the primary one. It is what "gone" scopes itself to, so it
	// must not name a storefront the run never reached.
	checked []string
}

// confirmGone re-asks the storefronts in countries about ids the primary one
// reported absent, and returns the ones that answered somewhere.
//
// An app listed in one country is still an app: calling it removed because the
// storefront this run happened to use cannot see it buries a live listing. Of
// 200 ids the pipeline had classified as dead, two were alive elsewhere -- one
// only in Russia, and one in every market probed except the United States it
// was run from. At that rate, thousands across the catalog.
//
// "Answered" means the listing is readable, which is what a genre index needs.
// It is a weaker claim than installable: an app can be listed in a market it
// is not offered in, and `gpscrape availability` is what separates those.
func confirmGone(ctx context.Context, c *common, absent, countries []string,
	progress io.Writer,
) (confirmResult, error) {
	res := confirmResult{foundIn: map[string]aliveElsewhere{}, unconfirmed: map[string]error{}}
	if len(absent) == 0 || len(countries) == 0 {
		return res, nil
	}

	pending := slices.Clone(absent)
	for _, cc := range countries {
		if cc == c.country || len(pending) == 0 {
			continue
		}
		res.checked = append(res.checked, cc)
		var next []string
		for d, err := range c.client().DigestsSeq(ctx, slices.Values(pending),
			googleplayscraper.DigestOptions{Lang: c.lang, Country: cc, Concurrency: c.concurrency}) {
			if err != nil {
				return res, err
			}
			switch {
			case d.Err != nil:
				// Still pending -- a later storefront can still rescue it --
				// but marked, so that "absent from every storefront asked" is
				// never inferred from a storefront that did not answer.
				res.unconfirmed[d.AppID] = d.Err
				next = append(next, d.AppID)
			case !d.Listed:
				next = append(next, d.AppID)
			default:
				delete(res.unconfirmed, d.AppID)
				res.foundIn[d.AppID] = aliveElsewhere{genre: d.GenreID, country: cc}
			}
		}
		if progress != nil {
			_, _ = fmt.Fprintf(progress, "%s: %d of %d absent ids answered\n",
				cc, len(pending)-len(next), len(pending))
		}
		pending = next
	}
	return res, nil
}

// splitCountries parses a comma-separated country list, lowercased and
// deduplicated, dropping blanks.
func splitCountries(spec string) []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range strings.Split(spec, ",") {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}
