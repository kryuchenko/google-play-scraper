// Command gpscrape reads public Google Play Store data and writes it to stdout
// as newline-delimited JSON.
//
//	gpscrape app com.spotify.music
//	gpscrape search "photo editor" -num 50
//	gpscrape reviews com.spotify.music -limit 500 | jq -r .text
//	gpscrape availability com.spotify.music -countries us,de,jp
//	gpscrape catalog -shards 0-9 > ids.txt
//
// One JSON object per line, so the output composes with jq, sort, and
// anything else that reads a stream. Commands that page (reviews, catalog)
// write as they go rather than at the end, which matters when the run is long
// enough that you want to watch it or stop it partway.
//
// Interrupting is a first-class operation, not an abort: SIGINT cancels the
// context, in-flight requests unwind, and what was already written stays
// valid. A catalog sweep is 83k requests, so being able to stop one without
// losing it is the difference between a usable tool and one you only run in a
// screen session.
//
// Deliberately stdlib-only. The root module is zero-dependency and a test
// enforces it; keeping this command in the same module means it ships and
// versions with the library rather than needing its own release. That
// constraint is why there is no cobra, no colour, and no progress bar.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	googleplayscraper "github.com/kryuchenko/google-play-scraper/v2"
)

// version is stamped at build time by the release workflow. An unstamped
// build says so rather than claiming a version it does not have -- a bug
// report that names the wrong build is worse than one that names none.
var version = "devel"

const usage = `gpscrape reads public Google Play Store data as newline-delimited JSON.

Usage:
  gpscrape <command> [flags] [arguments]

Commands:
  app <appID>..         app details (several apps ride in one request)
  search <term>         search results
  list [category]       a store collection: top charts, or what is new
  reviews <appID>       reviews, newest first unless -sort says otherwise
  similar <appID>       apps Google lists as similar
  developer <name|id>   a developer's apps
  permissions <appID>.. requested permissions (several apps ride in one request)
  datasafety <appID>    the data-safety declaration
  suggest <term>        search autocomplete
  categories            store categories; -kind game lists the game genres
  availability <appID>  per-country availability
  catalog <verb>        the store's app list: check, new, size, genres, sweep, apps, diff, ids
  sync                  alias for: catalog sweep
  version               print the build version

Common flags:
  -lang        language code (default "en")
  -country     country code (default "us")
  -throttle    minimum interval between request starts (default 200ms)
  -concurrency parallel requests where a command supports it (default 4)
  -timeout     overall deadline; 0 means none

Run "gpscrape <command> -h" for the flags a command adds.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	cmd := os.Args[1]
	if cmd == "-version" || cmd == "--version" || cmd == "version" {
		fmt.Println("gpscrape", version)
		return
	}
	if cmd == "-h" || cmd == "--help" || cmd == "help" {
		_, _ = fmt.Fprint(os.Stdout, usage)
		return
	}

	run, ok := commands[cmd]
	if !ok {
		fmt.Fprintf(os.Stderr, "gpscrape: unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}

	if err := run(os.Args[2:]); err != nil {
		// A cancelled run is how stopping is spelled, not a failure. Whatever
		// was written before the interrupt is complete lines of valid JSON.
		if errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "gpscrape: interrupted")
			os.Exit(130)
		}
		fmt.Fprintf(os.Stderr, "gpscrape: %v\n", err)
		os.Exit(1)
	}
}

type commandFunc func(args []string) error

var commands = map[string]commandFunc{
	"app":          cmdApp,
	"search":       cmdSearch,
	"list":         cmdList,
	"reviews":      cmdReviews,
	"similar":      cmdSimilar,
	"developer":    cmdDeveloper,
	"permissions":  cmdPermissions,
	"datasafety":   cmdDataSafety,
	"suggest":      cmdSuggest,
	"categories":   cmdCategories,
	"availability": cmdAvailability,
	"catalog":      cmdCatalogGroup,
	"sync":         cmdSync,
}

// common holds the flags every command accepts and builds the client and
// context from them. Keeping it in one place is what stops the commands from
// drifting into subtly different defaults.
type common struct {
	fs          *flag.FlagSet
	args        []string
	lang        string
	country     string
	throttle    time.Duration
	adaptive    bool
	concurrency int
	cached      *googleplayscraper.Client
	timeout     time.Duration
}

func newCommon(name string) *common {
	c := &common{fs: flag.NewFlagSet(name, flag.ExitOnError)}
	c.fs.StringVar(&c.lang, "lang", "en", "language code")
	c.fs.StringVar(&c.country, "country", "us", "country code")
	// Google throttles aggressively, so an unthrottled default would hand new
	// users a blocked IP as their first experience.
	c.fs.DurationVar(&c.throttle, "throttle", 200*time.Millisecond, "minimum interval between request starts")
	c.fs.BoolVar(&c.adaptive, "adaptive", false,
		"find a safe request rate instead of using -throttle as a fixed interval")
	c.fs.IntVar(&c.concurrency, "concurrency", 4, "parallel requests where supported")
	c.fs.DurationVar(&c.timeout, "timeout", 0, "overall deadline (0 = none)")
	return c
}

// client returns the one client this command run uses.
//
// It is memoised, and that is load-bearing rather than an optimisation. The
// throttle, the adaptive controller and the retry budget are all per-Client
// state, so a fresh client per call means a fresh throttle per call: reviews
// over several languages built one client per language and fired every
// language's first request immediately. At -langs all that is 71 requests
// back to back with -throttle set to anything at all, which is the blocked
// address this flag exists to avoid.
func (c *common) client() *googleplayscraper.Client {
	if c.cached != nil {
		return c.cached
	}
	opts := []googleplayscraper.ClientOption{
		googleplayscraper.WithThrottle(c.throttle),
		googleplayscraper.WithConcurrency(c.concurrency),
		// Retry is on by default here, unlike in the library, because a
		// command-line run is unattended: a catalog sweep is 83k requests over
		// hours, and without this every transient 503 is a shard of ~44 app
		// ids lost from the result with nothing but a line on stderr to show
		// for it. Retry-After is honoured because Google sends it on 429 and
		// coming back early is how a temporary limit becomes a longer one.
		googleplayscraper.WithRetry(googleplayscraper.RetryPolicy{
			MaxAttempts:       3,
			RespectRetryAfter: true,
		}),
	}
	if c.adaptive {
		// -throttle becomes the floor rather than the interval: the caller
		// still says how fast is too fast, the controller finds where under
		// that the server is comfortable. It starts at two seconds, so a run
		// that is going to be refused is refused gently.
		opts = append(opts, googleplayscraper.WithAdaptiveThrottle(
			googleplayscraper.AdaptivePolicy{Min: c.throttle, Max: 2 * time.Second}))
	}
	c.cached = googleplayscraper.NewClient(opts...)
	return c.cached
}

// context returns a context cancelled by SIGINT/SIGTERM and, if -timeout was
// given, by the deadline. The stop func must be called by the caller.
func (c *common) context() (context.Context, context.CancelFunc) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	if c.timeout <= 0 {
		return ctx, stop
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	return ctx, func() { cancel(); stop() }
}

// parse reads flags and positional arguments in any order. The flag package
// stops at the first non-flag argument, so a plain Parse would silently ignore
// everything after it -- `gpscrape availability com.x -countries us` would
// sweep all 177 countries and never say why. Re-parsing after consuming each
// positional is the standard way around that.
// noArgs rejects positional arguments for a command that takes none.
//
// Without it a dropped flag name is silent: `catalog ids 0-9` reads naturally,
// parses cleanly, discards the "0-9" and sweeps all 83,445 shards -- four
// hours and eighteen gigabytes instead of ten shards. The dispatcher already
// refuses a leading flag where a verb belongs, for the same reason; this is
// the other half of that guard.
func (c *common) noArgs(name string) error {
	if len(c.args) == 0 {
		return nil
	}
	// No example flag: this guard covers seven verbs and only one of them
	// takes -shards, so naming it sent the other six somewhere that does not
	// exist. `-h` lists the flags that do.
	return fmt.Errorf("%s takes no arguments, got %q; flags need their names -- see `%s -h`",
		name, strings.Join(c.args, " "), name)
}

func (c *common) parse(args []string) error {
	if err := c.fs.Parse(args); err != nil {
		return err
	}
	for c.fs.NArg() > 0 {
		c.args = append(c.args, c.fs.Arg(0))
		if err := c.fs.Parse(c.fs.Args()[1:]); err != nil {
			return err
		}
	}
	return nil
}

// arg returns the n-th positional argument, or an error naming what was
// expected. Commands that take an argument are useless without it, and a
// generic "missing argument" tells the user nothing about which.
func (c *common) arg(n int, what string) (string, error) {
	if len(c.args) <= n {
		return "", fmt.Errorf("%s: missing %s\n\nusage: gpscrape %s [flags] <%s>",
			c.fs.Name(), what, c.fs.Name(), what)
	}
	return c.args[n], nil
}

// progressTo returns the writer for human-facing progress, or nil when stderr
// is not a terminal. In a pipeline the counter is noise that ends up in
// whatever collects the log.
func progressTo(w *os.File) io.Writer {
	info, err := w.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return nil
	}
	return w
}

// emit writes one JSON object per line.
//
// Buffered when the output is a pipe or a file, unbuffered when it is a
// terminal. json.Encoder issues one Write per value, so writing straight to
// stdout costs a syscall per record: measured over 100,000 records that is
// 74ms against 12ms buffered, six times the cost, and a catalog sweep emits
// 3.7 million of them.
//
// The terminal case stays unbuffered on purpose. What makes the paging
// commands watchable is that a line appears when it is produced, and a 64KB
// buffer would hold thousands of them back. A pipe has no such expectation and
// wants the throughput.
//
// Flush is not optional: buffered output is lost if it is never called. SIGINT
// here cancels the context rather than killing the process, so the deferred
// flush in emitAll and in the paging commands runs on that path too.
type emitter struct {
	enc *json.Encoder
	buf *bufio.Writer // nil when writing straight through
	w   io.Writer     // the same destination enc writes to, for raw lines
}

func newEmitter(w io.Writer) *emitter {
	var buf *bufio.Writer
	if f, ok := w.(*os.File); !ok || progressTo(f) == nil {
		buf = bufio.NewWriterSize(w, 64<<10)
		w = buf
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false) // review text is full of & and <; escaping them helps nobody
	return &emitter{enc: enc, buf: buf, w: w}
}

func (e *emitter) emit(v any) error {
	if err := e.enc.Encode(v); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}

// raw writes one bare line, for an output that is a list of ids rather than a
// list of records. It shares emit's buffer, so mixing the two keeps both the
// ordering and the throughput.
func (e *emitter) raw(line string) error {
	if _, err := io.WriteString(e.w, line+"\n"); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}

// flush writes anything the buffer still holds. Safe to call on an unbuffered
// emitter and safe to call twice.
func (e *emitter) flush() error {
	if e.buf == nil {
		return nil
	}
	if err := e.buf.Flush(); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}

// ---- commands ----

func cmdApp(args []string) error {
	c := newCommon("app")
	if err := c.parse(args); err != nil {
		return err
	}
	if _, err := c.arg(0, "appID"); err != nil {
		return err
	}

	ctx, stop := c.context()
	defer stop()

	opts := googleplayscraper.AppOptions{Lang: c.lang, Country: c.country}

	if len(c.args) == 1 {
		app, err := c.client().App(ctx, c.args[0], opts)
		if err != nil {
			return err
		}
		return emitOne(app)
	}

	// Several apps ride in each request, over the RPC the details page is
	// built from rather than the page itself. An app that fails is named on
	// stderr so it stays out of the NDJSON stream on stdout.
	e := newEmitter(os.Stdout)
	defer func() { _ = e.flush() }()

	var failed int
	for _, r := range c.client().AppsMany(ctx, c.args, opts) {
		if r.Err != nil {
			failed++
			_, _ = fmt.Fprintf(os.Stderr, "%s: %v\n", r.AppID, r.Err)
			continue
		}
		if err := e.emit(r.App); err != nil {
			return err
		}
	}
	if failed == len(c.args) {
		return fmt.Errorf("all %d apps failed", failed)
	}
	return e.flush()
}

func cmdSearch(args []string) error {
	c := newCommon("search")
	num := c.fs.Int("num", 20, "maximum results (clamped to 250)")
	price := c.fs.String("price", "all", `"free", "paid" or "all"`)
	full := c.fs.Bool("full", false, "fetch full details for every result (one request each)")
	if err := c.parse(args); err != nil {
		return err
	}
	term, err := c.arg(0, "term")
	if err != nil {
		return err
	}

	ctx, stop := c.context()
	defer stop()

	results, err := c.client().Search(ctx, googleplayscraper.SearchOptions{
		Term: term, Lang: c.lang, Country: c.country,
		Num: *num, Price: *price, FullDetail: *full,
	})
	if err != nil {
		return err
	}
	return emitAll(results)
}

// reviewRecord is a review plus the corpus it came from.
//
// The language is not decoration. Reviews are the one place where hl
// partitions rather than filters -- the corpora do not overlap -- so a
// multi-language read produces a union whose parts are otherwise
// indistinguishable once merged. Without this field a caller cannot tell which
// slice a review arrived in, cannot re-fetch it, and cannot tell a gap in
// coverage from a gap in the store.
type reviewRecord struct {
	googleplayscraper.Review
	Lang string `json:"lang"`
}

func cmdReviews(args []string) error {
	c := newCommon("reviews")
	limit := c.fs.Int("limit", 200, "stop after this many reviews (0 = no limit); per language when -langs is given")
	sortBy := c.fs.String("sort", "newest", `"newest", "rating" or "helpfulness"`)
	score := c.fs.Int("score", 0, "keep only this star rating (1-5; 0 = all)")
	langs := c.fs.String("langs", "", `read several review corpora: "en,ru,de", or "all" for every `+
		`measured one. Reviews do not overlap between languages, so this is how to read all of them`)
	if err := c.parse(args); err != nil {
		return err
	}
	appID, err := c.arg(0, "appID")
	if err != nil {
		return err
	}
	sortVal, err := parseSort(*sortBy)
	if err != nil {
		return err
	}
	// The library ignores a score outside 1..5 and returns everything, so a
	// typo produced unfiltered data under a flag that said it was filtered --
	// silently, and with exit 0. -sort already refuses what it cannot honour.
	if *score < 0 || *score > 5 {
		return fmt.Errorf("-score %d: a star rating is 1 to 5, or 0 for all", *score)
	}
	list, err := reviewLangs(*langs, c.lang)
	if err != nil {
		return err
	}
	// -country is accepted because every command takes it, and for reviews
	// Google ignores it. Saying so is better than letting someone believe they
	// filtered by market: checked on kz.kaspi.mobile, a bank used almost
	// entirely from Kazakhstan, where ru/kz and ru/us return the same reviews
	// id for id.
	var countrySet bool
	c.fs.Visit(func(f *flag.Flag) {
		if f.Name == "country" {
			countrySet = true
		}
	})
	if countrySet {
		// Not behind progressTo: that gate is for progress counters, which
		// only a person watching wants. The reader who needs this line is a
		// script looping over country codes and labelling each review with
		// the country it was "fetched from" -- and it is non-interactive by
		// definition, so gating on a terminal hid it from exactly the caller
		// it is written for.
		{
			w := os.Stderr
			_, _ = fmt.Fprintf(w, "note: -country has no effect on reviews. Google serves the same "+
				"reviews for every market -- checked on an app used almost entirely from one country -- "+
				"and there is no country anywhere in a review. Reviews can only be selected by "+
				"language: use -langs\n")
		}
	}

	ctx, stop := c.context()
	defer stop()

	// Streamed rather than collected: on a popular app the sequence is
	// effectively unbounded, and -limit is the caller's stopping rule rather
	// than a size to allocate up front.
	e := newEmitter(os.Stdout)
	// Deferred as well as returned: an interrupt unwinds through here, and
	// what was already produced must reach the pipe.
	defer func() { _ = e.flush() }()

	opts := googleplayscraper.ReviewOptions{
		Country: c.country, Sort: sortVal, FilterScore: *score,
	}

	// One language is the ordinary case and stays exactly as it was.
	if len(list) == 1 {
		opts.Lang = list[0]
		if _, _, err := emitReviews(ctx, c, e, appID, opts, *limit, nil); err != nil {
			return err
		}
		return e.flush()
	}

	// Several corpora, read in the order asked for.
	//
	// Sequential, because the throttle caps the request rate across the whole
	// client: running the languages in parallel would not raise the ceiling,
	// only the number of things in flight under it. In exchange the output is
	// deterministic, which matters when the consumer is diffing today's read
	// against yesterday's.
	//
	// Deduplicated by review id even though the measured corpora do not
	// overlap, because some codes are aliases -- tg and tk are served the
	// Russian corpus verbatim -- and a caller passing its own list has no way
	// to know which.
	seen := make(map[string]struct{})
	var total, dups int
	progress := progressTo(os.Stderr)
	for _, lang := range list {
		opts.Lang = lang
		n, skipped, err := emitReviews(ctx, c, e, appID, opts, *limit, seen)
		if err != nil {
			return err
		}
		total += n
		dups += skipped
		if progress != nil {
			_, _ = fmt.Fprintf(progress, "%s: %d reviews (%d total)\n", lang, n, total)
		}
	}
	if progress != nil && dups > 0 {
		// Worth saying rather than hiding: it means two of the names given
		// are one corpus, so the run cost more requests than it had to.
		_, _ = fmt.Fprintf(progress,
			"%d reviews already seen under another language (aliased corpora)\n", dups)
	}
	return e.flush()
}

// emitReviews streams one corpus. seen, when non-nil, suppresses ids already
// emitted and is what makes a list containing two names for one corpus safe.
func emitReviews(ctx context.Context, c *common, e *emitter, appID string,
	opts googleplayscraper.ReviewOptions, limit int, seen map[string]struct{},
) (emitted, skipped int, err error) {
	for r, rerr := range c.client().ReviewsSeq(ctx, appID, opts) {
		if rerr != nil {
			return emitted, skipped, rerr
		}
		if seen != nil {
			if _, dup := seen[r.ID]; dup {
				// An alias for a corpus already read. Skipping rather than
				// stopping means the limit is still reached, from further
				// down the same corpus, so the caller gets what they asked
				// for instead of a short answer.
				skipped++
				continue
			}
			seen[r.ID] = struct{}{}
		}
		if eerr := e.emit(reviewRecord{Review: r, Lang: opts.Lang}); eerr != nil {
			return emitted, skipped, eerr
		}
		emitted++
		if limit > 0 && emitted >= limit {
			break
		}
	}
	return emitted, skipped, nil
}

// reviewLangs resolves -langs against -lang.
func reviewLangs(langs, single string) ([]string, error) {
	// Normalise before anything else. The keyword used to be matched on the
	// raw string while every real code went through this, so -langs ALL fell
	// through to the comma split, became the single code "all", and was sent
	// as hl=all: one bogus corpus, exit 0, no warning, instead of 71.
	langs = strings.ToLower(strings.TrimSpace(langs))
	if langs == "" {
		return []string{single}, nil
	}
	if langs == "all" {
		return slices.Clone(googleplayscraper.ReviewLanguages), nil
	}
	var out []string
	seen := map[string]struct{}{}
	for _, l := range strings.Split(langs, ",") {
		l = strings.ToLower(strings.TrimSpace(l))
		if l == "" {
			continue
		}
		if _, dup := seen[l]; dup {
			continue
		}
		seen[l] = struct{}{}
		out = append(out, l)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("-langs: no languages in %q", langs)
	}
	return out, nil
}

// collections names the store clusters the vyAe2 RPC answers to. NEW_FREE is
// the interesting one for anybody tracking what has appeared: measured across
// the seventeen GAME_* categories it returns 1,230 apps in 17 requests, 99% of
// them published within the last thirty days.
var collections = map[string]googleplayscraper.Collection{
	"top_free":       googleplayscraper.CollectionTopFree,
	"top_paid":       googleplayscraper.CollectionTopPaid,
	"grossing":       googleplayscraper.CollectionGrossing,
	"new_free":       googleplayscraper.CollectionNewFree,
	"new_paid":       googleplayscraper.CollectionNewPaid,
	"movers_shakers": googleplayscraper.CollectionMoversShakers,
}

func collectionNames() string {
	names := make([]string, 0, len(collections))
	for k := range collections {
		names = append(names, k)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func cmdList(args []string) error {
	c := newCommon("list")
	collection := c.fs.String("collection", "top_free", "one of: "+collectionNames())
	num := c.fs.Int("num", 200, "maximum apps (clamped to 660; Google returns ~200 in practice)")
	full := c.fs.Bool("full", false, "fetch full details for each app (one extra batched request per 32)")
	if err := c.parse(args); err != nil {
		return err
	}

	col, ok := collections[strings.ToLower(*collection)]
	if !ok {
		return fmt.Errorf("unknown collection %q; want one of: %s", *collection, collectionNames())
	}

	// The category is optional: without one the store answers for applications
	// as a whole, which is what the website shows on its front page.
	category := googleplayscraper.Category("")
	if len(c.args) > 0 {
		category = googleplayscraper.Category(strings.ToUpper(c.args[0]))
	}

	ctx, stop := c.context()
	defer stop()

	results, err := c.client().List(ctx, googleplayscraper.ListOptions{
		Collection: col,
		Category:   category,
		Num:        *num,
		FullDetail: *full,
		Lang:       c.lang,
		Country:    c.country,
	})
	if err != nil {
		return err
	}
	return emitAll(results)
}

func cmdSimilar(args []string) error {
	c := newCommon("similar")
	if err := c.parse(args); err != nil {
		return err
	}
	appID, err := c.arg(0, "appID")
	if err != nil {
		return err
	}
	ctx, stop := c.context()
	defer stop()

	results, err := c.client().Similar(ctx, googleplayscraper.SimilarOptions{
		AppID: appID, Lang: c.lang, Country: c.country,
	})
	if err != nil {
		return err
	}
	return emitAll(results)
}

func cmdDeveloper(args []string) error {
	c := newCommon("developer")
	if err := c.parse(args); err != nil {
		return err
	}
	dev, err := c.arg(0, "developer")
	if err != nil {
		return err
	}
	ctx, stop := c.context()
	defer stop()

	results, err := c.client().Developer(ctx, googleplayscraper.DeveloperOptions{
		DevID: dev, Lang: c.lang, Country: c.country,
	})
	if err != nil {
		return err
	}
	return emitAll(results)
}

// permissionsRecord is the multi-app output shape. One app keeps emitting bare
// permission objects, which is what a caller piping into jq wants; several apps
// need to say which app each answer belongs to.
type permissionsRecord struct {
	AppID       string                         `json:"appId"`
	Permissions []googleplayscraper.Permission `json:"permissions,omitempty"`
	Error       string                         `json:"error,omitempty"`
}

func cmdPermissions(args []string) error {
	c := newCommon("permissions")
	if err := c.parse(args); err != nil {
		return err
	}
	if _, err := c.arg(0, "appID"); err != nil {
		return err
	}
	ctx, stop := c.context()
	defer stop()

	opts := googleplayscraper.PermissionsOptions{Lang: c.lang, Country: c.country}

	// One record per app, whether one app was asked for or fifty. An earlier
	// version emitted bare permission objects for a single app and per-app
	// records for several, which reads well in a demo and is a trap in a
	// script: `gpscrape permissions "$@" | jq -r .permission` works until the
	// day someone passes two ids, and then silently yields nulls. The sibling
	// `app` command has one shape; so does this one.
	//
	// Several apps ride in each request, so this costs one throttle interval
	// per 32 apps rather than one per app.
	results := c.client().PermissionsMany(ctx, c.args, opts)
	if err := emitAll(permissionsRecords(results)); err != nil {
		return err
	}
	// Every app still gets its line; the exit code is what tells a script the
	// run produced nothing usable, matching `app`.
	failed := 0
	for _, r := range results {
		if r.Err != nil {
			failed++
		}
	}
	if failed == len(results) {
		return fmt.Errorf("all %d apps failed", failed)
	}
	return nil
}

// permissionsRecords flattens per-app errors into the output shape. A chunk
// that failed must still emit a line for each of its apps: a caller diffing
// input against output would otherwise see apps silently vanish.
func permissionsRecords(results []googleplayscraper.PermissionsResult) []permissionsRecord {
	records := make([]permissionsRecord, len(results))
	for i, r := range results {
		records[i] = permissionsRecord{AppID: r.AppID, Permissions: r.Permissions}
		if r.Err != nil {
			records[i].Error = r.Err.Error()
		}
	}
	return records
}

func cmdDataSafety(args []string) error {
	c := newCommon("datasafety")
	if err := c.parse(args); err != nil {
		return err
	}
	appID, err := c.arg(0, "appID")
	if err != nil {
		return err
	}
	ctx, stop := c.context()
	defer stop()

	ds, err := c.client().DataSafety(ctx, googleplayscraper.DataSafetyOptions{
		AppID: appID, Lang: c.lang, Country: c.country,
	})
	if err != nil {
		return err
	}
	return emitOne(ds)
}

func cmdSuggest(args []string) error {
	c := newCommon("suggest")
	if err := c.parse(args); err != nil {
		return err
	}
	term, err := c.arg(0, "term")
	if err != nil {
		return err
	}
	ctx, stop := c.context()
	defer stop()

	suggestions, err := c.client().Suggest(ctx, googleplayscraper.SuggestOptions{
		Term: term, Lang: c.lang, Country: c.country,
	})
	if err != nil {
		return err
	}
	return emitAll(suggestions)
}

func cmdCategories(args []string) error {
	c := newCommon("categories")
	kind := c.fs.String("kind", "all", `which categories: "all", "game" or "app"`)
	if err := c.parse(args); err != nil {
		return err
	}
	ctx, stop := c.context()
	defer stop()

	cats, err := c.client().Categories(ctx, googleplayscraper.CategoriesOptions{
		Lang: c.lang, Country: c.country,
	})
	if err != nil {
		return err
	}

	// One output shape whichever filter is chosen: a category id per line, so
	// the same pipeline reads all three.
	switch strings.ToLower(*kind) {
	case "all":
	case "game":
		cats = filterCategories(cats, func(x googleplayscraper.Category) bool { return x.IsGame() })
	case "app":
		cats = filterCategories(cats, func(x googleplayscraper.Category) bool { return !x.IsGame() })
	default:
		return fmt.Errorf("unknown -kind %q; want all, game or app", *kind)
	}
	return emitAll(cats)
}

func filterCategories(in []googleplayscraper.Category, keep func(googleplayscraper.Category) bool) []googleplayscraper.Category {
	out := in[:0:0]
	for _, c := range in {
		if keep(c) {
			out = append(out, c)
		}
	}
	return out
}

func cmdAvailability(args []string) error {
	c := newCommon("availability")
	countries := c.fs.String("countries", "", "comma-separated country codes (default: all 177)")
	if err := c.parse(args); err != nil {
		return err
	}
	appID, err := c.arg(0, "appID")
	if err != nil {
		return err
	}

	ctx, stop := c.context()
	defer stop()

	var list []string
	if *countries != "" {
		list = strings.Split(*countries, ",")
	}

	// The default sweep is one request per country, so progress goes to stderr
	// -- stdout stays a clean JSON stream even when a human is watching.
	var progress func(googleplayscraper.AvailabilityProgress)
	if w := progressTo(os.Stderr); w != nil {
		progress = func(p googleplayscraper.AvailabilityProgress) {
			_, _ = fmt.Fprintf(w, "\r%d/%d countries", p.DoneCount, p.TotalCount)
		}
	}

	res, err := c.client().Availability(ctx, appID, googleplayscraper.AvailabilityOptions{
		Countries: list, Lang: c.lang, Concurrency: c.concurrency,
		Progress: progress,
	})
	if progress != nil {
		fmt.Fprintln(os.Stderr)
	}
	if err != nil {
		return err
	}
	return emitOne(res)
}

func catalogIDs(args []string) error {
	c := newCommon("catalog ids")
	shards := c.fs.String("shards", "", `shard range, e.g. "0-99" or "0,5,7" (default: every shard)`)
	limit := c.fs.Int("limit", 0, "stop after this many ids (0 = no limit)")
	if err := c.parse(args); err != nil {
		return err
	}

	if err := c.noArgs("catalog ids"); err != nil {
		return err
	}

	subset, err := parseShards(*shards)
	if err != nil {
		return err
	}

	ctx, stop := c.context()
	defer stop()

	// A full sweep is ~83k requests and tens of gigabytes. Ids are written as
	// they arrive so a run can be watched, stopped, and resumed from its own
	// output rather than started over.
	out := os.Stdout
	// Atomic because OnShardDone below runs on a worker goroutine while the
	// range loop increments from the consumer's.
	var n atomic.Int64
	started := time.Now()
	for pkg, err := range c.client().CatalogSeq(ctx, googleplayscraper.CatalogOptions{
		Concurrency: c.concurrency,
		Shards:      subset,
		OnShardError: func(idx int, _ string, err error) {
			fmt.Fprintf(os.Stderr, "shard %d: %v\n", idx, err)
		},
		OnShardDone: func(idx int, _ string, _ int) {
			if idx%100 == 0 {
				fmt.Fprintf(os.Stderr, "shard %d, %d ids, %s elapsed\n",
					idx, n.Load(), time.Since(started).Round(time.Second))
			}
		},
	}) {
		// Terminal errors only; a shard that failed went to OnShardError and
		// the sweep carried on past it.
		if err != nil {
			return err
		}
		if _, werr := fmt.Fprintln(out, pkg); werr != nil {
			return fmt.Errorf("write output: %w", werr)
		}
		// Incremented unconditionally: folding this into the limit check
		// would skip it entirely when -limit is 0, and the final count would
		// always report zero.
		count := int(n.Add(1))
		if *limit > 0 && count >= *limit {
			break
		}
	}
	_, _ = fmt.Fprintf(os.Stderr, "%d ids in %s\n", n.Load(), time.Since(started).Round(time.Second))
	return nil
}

// ---- helpers ----

// emitOne writes a single record and flushes. The flush is why this exists:
// `return newEmitter(os.Stdout).emit(v)` silently discards buffered output.
func emitOne(v any) error {
	e := newEmitter(os.Stdout)
	if err := e.emit(v); err != nil {
		_ = e.flush()
		return err
	}
	return e.flush()
}

func emitAll[T any](items []T) error {
	e := newEmitter(os.Stdout)
	for _, it := range items {
		if err := e.emit(it); err != nil {
			_ = e.flush()
			return err
		}
	}
	return e.flush()
}

func parseSort(s string) (googleplayscraper.Sort, error) {
	switch strings.ToLower(s) {
	case "newest":
		return googleplayscraper.SortNewest, nil
	case "rating":
		return googleplayscraper.SortRating, nil
	case "helpfulness":
		return googleplayscraper.SortHelpfulness, nil
	default:
		return 0, fmt.Errorf("unknown sort %q: want newest, rating or helpfulness", s)
	}
}

// parseShards accepts "0-99", "0,5,7", or a mixture, because both forms come
// up: a range when sampling the catalog, a list when re-running the shards a
// previous sweep reported as failed.
func parseShards(spec string) ([]int, error) {
	if spec == "" {
		return nil, nil
	}
	var out []int
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		lo, hi, isRange := strings.Cut(part, "-")
		if !isRange {
			n, err := strconv.Atoi(part)
			if err != nil {
				return nil, fmt.Errorf("bad shard %q: %w", part, err)
			}
			out = append(out, n)
			continue
		}
		start, err := strconv.Atoi(strings.TrimSpace(lo))
		if err != nil {
			return nil, fmt.Errorf("bad shard range %q: %w", part, err)
		}
		end, err := strconv.Atoi(strings.TrimSpace(hi))
		if err != nil {
			return nil, fmt.Errorf("bad shard range %q: %w", part, err)
		}
		if end < start {
			return nil, fmt.Errorf("bad shard range %q: end is before start", part)
		}
		// An unbounded range is 8 bytes an element with nothing to stop it:
		// -shards 0-9999999999 allocated its way to an OOM before the first
		// request. The catalog is 83,445 shards and the caller can only have
		// meant a typo.
		if end-start >= shardRangeMax {
			return nil, fmt.Errorf("shard range %q spans %d shards; the catalog has about 83,445",
				part, end-start+1)
		}
		for i := start; i <= end; i++ {
			out = append(out, i)
		}
	}
	// Empty here means the caller named shards and none of them parsed to
	// anything -- `-shards " "`, `-shards ","`. Returning nil would mean
	// "every shard" to CatalogSeq, so a stray space swept the whole catalog.
	if len(out) == 0 {
		return nil, fmt.Errorf("-shards %q names no shards; leave it off to sweep all of them", spec)
	}
	// Sorted and deduplicated: -shards 3,3,3 fetched shard 3 three times and
	// emitted its ids three times.
	slices.Sort(out)
	out = slices.Compact(out)
	return out, nil
}

// shardRangeMax bounds a single -shards range. Generous against the ~83,445
// shards a generation actually has, and far below anything that allocates
// dangerously.
const shardRangeMax = 1 << 20
