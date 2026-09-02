package main

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"iter"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	googleplayscraper "github.com/kryuchenko/google-play-scraper"
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
		return fmt.Errorf("catalog: need a verb\n\n%s", catalogUsage)
	}
	// A leading flag is a missing verb, not a shorthand for one.
	//
	// This did route to `ids`, because `catalog -shards 0-99` reads naturally
	// and used to work. It is a trap: `ids` with no -shards sweeps all 83,445
	// of them, so any mistyped verb -- or a wrapper that puts a global flag
	// first -- became a four-hour, eighteen-gigabyte run instead of an error
	// message. Nothing about the shorthand is worth that.
	verb := args[0]
	if strings.HasPrefix(verb, "-") {
		return fmt.Errorf("catalog: %s is a flag, not a verb; did you mean `catalog ids %s`?\n\n%s",
			verb, strings.Join(args, " "), catalogUsage)
	}
	run, ok := verbs[verb]
	if !ok {
		return fmt.Errorf("catalog: unknown verb %q\n\n%s", verb, catalogUsage)
	}
	return run(args[1:])
}

const catalogUsage = `Usage: gpscrape catalog <verb> [flags]

  check    has Google republished the catalog?          2 requests
  new      apps the store lists as recently published   17 requests
  size     how many apps there are, give or take 1%     ~900 requests
  genres   genre changes and removals, from a snapshot  ~3.3k requests
  sweep    the complete id list, and the exact count    83k requests
  apps     the id list, by genre, from what is on disk  none
  diff     compare two snapshots already on disk        none
  ids      stream ids without keeping any state         as many as you ask for
`

// ---- check ----

type checkRecord struct {
	Generation string  `json:"generation"`
	Built      string  `json:"built,omitempty"`
	AgeHours   int     `json:"ageHours"`
	Shards     int     `json:"shards"`
	Have       string  `json:"have,omitempty"`
	HaveSample float64 `json:"haveSamplePct,omitempty"`
	UpToDate   bool    `json:"upToDate"`
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

type genreRecord struct {
	AppID   string `json:"appId"`
	GenreID string `json:"genreId,omitempty"`
	Genre   string `json:"genre,omitempty"`
	Was     string `json:"was,omitempty"`
	Change  string `json:"change"` // first_seen | changed | gone | same
	Error   string `json:"error,omitempty"`
}

func catalogGenres(args []string) error {
	c := newCommon("catalog genres")
	dir := c.fs.String("dir", "catalog", "directory holding the snapshot to read")
	from := c.fs.String("ids", "", "read ids from this file instead of the snapshot (- for stdin)")
	all := c.fs.Bool("all", false, "emit every app, not only the changes")
	if err := c.parse(args); err != nil {
		return err
	}
	if err := c.noArgs("catalog genres"); err != nil {
		return err
	}
	ctx, stop := c.context()
	defer stop()

	ids, closeIDs, coverage, err := idSource(*dir, *from)
	if err != nil {
		return err
	}
	defer closeIDs()

	// What each app's genre was last time, so a change can be named as one.
	prev, prevPath := loadGenres(*dir)

	e := newEmitter(os.Stdout)
	defer func() { _ = e.flush() }()

	// Start from what is already known rather than from nothing.
	//
	// This map is written over the genre table wholesale at the end, so
	// anything missing from it is deleted. Building it only from what this run
	// saw made two ordinary situations destructive: one transient 503 dropped
	// an app from the table, and running with -ids on a subset shrank the
	// table to that subset. Either way the app comes back as "first_seen" next
	// time, which is a change that did not happen -- and at catalog scale a
	// 0.1% error rate is thousands of them a day.
	//
	// It is the same reasoning the sweep already applies to failed shards:
	// quietly wrong deltas are worse than admitting the pass was incomplete.
	cur := maps.Clone(prev)
	if cur == nil {
		cur = make(map[string]string)
	}
	var seen, changed, gone, failed int
	var lastErr error
	progress := progressTo(os.Stderr)

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
			return err
		}
		seen++
		rec := genreRecord{AppID: d.AppID, GenreID: d.GenreID, Genre: d.Genre}
		was, known := prev[d.AppID]
		rec.Was = was

		switch {
		case d.Err != nil:
			// Keep whatever the table already said. A request that failed is
			// not evidence about the app.
			rec.Change, rec.Error = "error", d.Err.Error()
			failed++
			lastErr = d.Err
		case !d.Listed:
			rec.Change = "gone"
			gone++
			// Explicit now that cur starts as a copy: an app that no longer
			// resolves has to be taken out, not merely left unwritten.
			delete(cur, d.AppID)
		case !known:
			rec.Change = "first_seen"
			cur[d.AppID] = d.GenreID
		case was != d.GenreID:
			rec.Change = "changed"
			changed++
			cur[d.AppID] = d.GenreID
		default:
			rec.Change = "same"
			cur[d.AppID] = d.GenreID
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

	// The genre table is the useful by-product: a snapshot minus the ids that
	// no longer resolve, which is what the catalog actually is. Nothing else
	// publishes it.
	// A run in which nothing resolved is not a run that found nothing. Left to
	// save, it wrote an empty table over a good one and exited 0 -- reproduced
	// while rate-limited, where all 1,740 lookups failed and the summary still
	// read "1740 apps". This is the rule the sweep already applies to failed
	// shards, which had not been carried across.
	if failed == seen && seen > 0 {
		return fmt.Errorf("all %d lookups failed, so the genre table was left untouched; "+
			"the last error was: %v", seen, lastErr)
	}
	if err := saveGenres(prevPath, cur); err != nil {
		return err
	}
	if coverage > 0 {
		// The table now merges rather than overwrites, so reading a sample is
		// harmless -- but the counts above describe the sample, not the store,
		// and nothing else in the output says so.
		fmt.Fprintf(os.Stderr, "note: the snapshot covers %g%% of the catalog; "+
			"these counts are of that sample, not the store\n", coverage)
	}
	// failed is reported rather than folded into the total: "1740 apps" reads
	// as success, and a run where a tenth of them errored is not one.
	if failed > 0 {
		fmt.Fprintf(os.Stderr, "%d apps, %d changed, %d gone, %d could not be read -> %s\n",
			seen, changed, gone, failed, prevPath)
	} else {
		fmt.Fprintf(os.Stderr, "%d apps, %d changed, %d gone -> %s\n", seen, changed, gone, prevPath)
	}
	return e.flush()
}

// idSource yields ids from a file, from stdin, or from the newest snapshot.
// idSource opens the list of app ids to work through, and reports what share
// of the catalog it covers: 0 for a complete snapshot or a caller-supplied
// list, the sampled percentage otherwise. Callers report that, because a
// sample and the catalog are the same sorted list of ids from the outside and
// only the manifest knows the difference.
func idSource(dir, from string) (iter.Seq[string], func(), float64, error) {
	switch {
	case from == "-":
		return scannerSeq(os.Stdin), func() {}, 0, nil
	case from != "":
		f, err := os.Open(from)
		if err != nil {
			return nil, nil, 0, err
		}
		return scannerSeq(f), func() { _ = f.Close() }, 0, nil
	}

	m, ok := latestManifest(dir)
	if !ok {
		return nil, nil, 0, fmt.Errorf("no snapshot in %s; run `gpscrape catalog sweep -dir %s` first, "+
			"or pass -ids", dir, dir)
	}
	path := filepath.Join(dir, m.File)
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, 0, err
	}
	zr, err := gzip.NewReader(f)
	if err != nil {
		_ = f.Close()
		return nil, nil, 0, err
	}
	return scannerSeq(zr), func() { _ = zr.Close(); _ = f.Close() }, m.SamplePct, nil
}

// scannerSeq streams lines. The point of a sequence rather than a slice is that
// a catalog snapshot is 3.4 million ids, and the digest pass holds one pack per
// worker rather than all of them.
func scannerSeq(r interface{ Read([]byte) (int, error) }) iter.Seq[string] {
	return func(yield func(string) bool) {
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
	}
}

func genresPath(dir string) string { return filepath.Join(dir, "genres.tsv") }

func loadGenres(dir string) (map[string]string, string) {
	path := genresPath(dir)
	out := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		return out, path
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		id, genre, ok := strings.Cut(sc.Text(), "\t")
		if ok {
			out[id] = genre
		}
	}
	return out, path
}

func saveGenres(path string, genres map[string]string) error {
	ids := make([]string, 0, len(genres))
	for id := range genres {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	w := bufio.NewWriterSize(f, 1<<20)
	for _, id := range ids {
		if _, err := fmt.Fprintf(w, "%s\t%s\n", id, genres[id]); err != nil {
			_ = f.Close()
			return err
		}
	}
	if err := w.Flush(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ---- new ----

type newRecord struct {
	AppID    string `json:"appId"`
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
		return fmt.Errorf("unknown collection %q; want one of: %s", *collection, collectionNames())
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
	known, err := loadSignalIDs(logPath)
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
	lenc := json.NewEncoder(lw)

	e := newEmitter(os.Stdout)
	defer func() { _ = e.flush() }()

	now := time.Now().UTC().Format(time.RFC3339)
	var total, fresh int
	for _, cat := range categories {
		results, err := c.client().List(ctx, googleplayscraper.ListOptions{
			Collection: col, Category: cat, Num: *num,
			Lang: c.lang, Country: c.country,
		})
		if err != nil {
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
		return nil, fmt.Errorf("no categories in %q", spec)
	}
	return out, nil
}

func loadSignalIDs(path string) (map[string]bool, error) {
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
		if json.Unmarshal(sc.Bytes(), &rec) == nil && rec.AppID != "" {
			out[rec.AppID] = true
		}
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
type diffRecord struct {
	From    string       `json:"from"`
	To      string       `json:"to"`
	Added   int          `json:"added"`
	Removed int          `json:"removed"`
	Signal  *signalStats `json:"signal,omitempty"`
}

type signalStats struct {
	Observed          int     `json:"observed"`
	MatchedAdded      int     `json:"matchedAdded"`
	Recall            float64 `json:"recall"`
	Precision         float64 `json:"precision"`
	UnsignalledAdded  int     `json:"unsignalledAdded"`
	UnsignalledPerDay float64 `json:"unsignalledPerDay,omitempty"`
	DaysBetween       float64 `json:"daysBetween,omitempty"`
}

func catalogDiff(args []string) error {
	c := newCommon("catalog diff")
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
		return fmt.Errorf("catalog diff: give two snapshot files, or none to use the two newest in -dir")
	}

	oldIDs, err := readSnapshot(fromPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", fromPath, err)
	}
	newIDs, err := readSnapshot(toPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", toPath, err)
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

	d := diff(fromGen, toGen, oldIDs, newIDs)

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
	observed, err := loadSignalIDs(filepath.Join(dir, "signal.log"))
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
		return "", "", fmt.Errorf("need two snapshots in %s; found %d", dir, len(entries))
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
		return "", "", fmt.Errorf("need two readable manifests in %s; found %d", dir, len(snaps))
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
	HalfWidth    int     `json:"halfWidth"`
	RelativePct  float64 `json:"relativePct"`
	ShardsRead   int     `json:"shardsRead"`
	ShardsTotal  int     `json:"shardsTotal"`
	MeanPerShard float64 `json:"meanPerShard"`
	// Not omitempty: dispersionZ of exactly 0 is a perfectly uniform hash,
	// which is the healthiest reading there is, and dropping it from the
	// output makes the best case look like a missing field.
	Dispersion  float64 `json:"dispersion"`
	DispersionZ float64 `json:"dispersionZ"`
	TargetPct   float64 `json:"targetPct"`
	MetTarget   bool    `json:"metTarget"`
	HashUniform bool    `json:"hashUniform"`
	Warning     string  `json:"warning,omitempty"`
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
	if err := c.parse(args); err != nil {
		return err
	}

	if err := c.noArgs("catalog size"); err != nil {
		return err
	}

	p, err := parsePercent(*precision)
	if err != nil {
		return fmt.Errorf("-precision: %w", err)
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
		return 0, fmt.Errorf("%q is not a number", s)
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
		return 0, fmt.Errorf("%q is out of range; want something like 1%% or 0.5%%", s)
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
	AppID   string `json:"appId"`
	GenreID string `json:"genreId"`
}

func catalogApps(args []string) error {
	c := newCommon("catalog apps")
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

	table, path := loadGenres(*dir)
	if len(table) == 0 {
		return fmt.Errorf("no genre table in %s; run `gpscrape catalog sweep -dir %s` then "+
			"`gpscrape catalog genres -dir %s` to build one", *dir, *dir, *dir)
	}

	ids := make([]string, 0, len(table))
	for id, g := range table {
		if match(g) {
			ids = append(ids, id)
		}
	}
	// A pattern that matches nothing is almost always a typo -- lower case, a
	// plural, a genre that does not exist -- and the shape it used to take was
	// an empty file and exit 0, which a pipeline cannot tell from "the store
	// has no games". The count line that would have hinted at it goes to
	// stderr behind a terminal check, so the caller this output is written for
	// never saw it.
	if len(ids) == 0 && *genre != "" {
		return fmt.Errorf("no app in %s has a genre matching %q; "+
			"genre ids look like GAME_PUZZLE or PRODUCTIVITY, and `gpscrape categories` lists them",
			path, *genre)
	}
	// Sorted, because this output is compared against yesterday's by whoever
	// consumes it, and a map's order would make every line look changed.
	slices.Sort(ids)

	e := newEmitter(os.Stdout)
	defer func() { _ = e.flush() }()
	for _, id := range ids {
		if *idsOnly {
			if err := e.raw(id); err != nil {
				return err
			}
			continue
		}
		if err := e.emit(appRecord{AppID: id, GenreID: table[id]}); err != nil {
			return err
		}
	}
	if w := progressTo(os.Stderr); w != nil {
		_, _ = fmt.Fprintf(w, "%d of %d apps in %s\n", len(ids), len(table), path)
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
			return nil, fmt.Errorf("-genre %q: only a trailing * is supported", pattern)
		}
		return func(g string) bool { return strings.HasPrefix(g, prefix) }, nil
	default:
		if strings.Contains(pattern, "*") {
			return nil, fmt.Errorf("-genre %q: only a trailing * is supported", pattern)
		}
		return func(g string) bool { return g == pattern }, nil
	}
}
