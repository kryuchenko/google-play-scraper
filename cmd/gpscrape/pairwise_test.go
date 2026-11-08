package main

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/kryuchenko/google-play-scraper/records"
)

// All-pairs testing of the command line (Kaner, Bach and Pettichord, "Lessons
// Learned in Software Testing", lesson 59 onwards): every pair of factor
// levels -- command and -fields, -raw and a missing id, -lang and -adaptive --
// appears in at least one run, in a few dozen runs rather than the tens of
// thousands of the full product. Bugs that need two things to go wrong at
// once are the ones example-based tests miss, and most of them need only two.

// ---- the generator ----

// pairModel is what allPairs covers.
type pairModel struct {
	// levels[f] is how many levels factor f has.
	levels []int
	// ok judges a partial row, -1 meaning unset. It must be exact: a partial
	// row it accepts completes into a full row it accepts.
	ok func(row []int) bool
	// invalid marks the levels that are errors in themselves, PICT's
	// negative values: -lang en,de, an empty id. A row holds at most one,
	// since the first error ends the run before a second is looked at, and a
	// row holding one covers only the pairs that include it. Without that,
	// "permissions with a missing id" was covered by a run that stopped at
	// -country zz! and never asked the store anything.
	invalid func(f, v int) bool
}

// pairKey is one pair of factor levels: factor f1 at level v1 with factor f2
// at level v2, f1 < f2.
type pairKey struct{ f1, v1, f2, v2 int }

func pairOf(f, v, g, w int) pairKey {
	if f > g {
		f, v, g, w = g, w, f, v
	}
	return pairKey{f, v, g, w}
}

func (m pairModel) unset() []int {
	row := make([]int, len(m.levels))
	for i := range row {
		row[i] = -1
	}
	return row
}

// required lists, in a fixed order, the pairs a covering must hold: every
// pair ok accepts, except one of two invalid levels.
func (m pairModel) required() []pairKey {
	var out []pairKey
	for f := range m.levels {
		for g := f + 1; g < len(m.levels); g++ {
			for v := range m.levels[f] {
				for w := range m.levels[g] {
					row := m.unset()
					row[f], row[g] = v, w
					if m.ok(row) && (!m.invalid(f, v) || !m.invalid(g, w)) {
						out = append(out, pairKey{f, v, g, w})
					}
				}
			}
		}
	}
	return out
}

// covers returns the pairs a full row covers: all of them, or, in a row with
// an invalid level, the ones that include it.
func (m pairModel) covers(row []int) []pairKey {
	bad := -1
	for f, v := range row {
		if m.invalid(f, v) {
			bad = f
		}
	}
	var out []pairKey
	for f := range row {
		for g := f + 1; g < len(row); g++ {
			if bad < 0 || f == bad || g == bad {
				out = append(out, pairKey{f, row[f], g, row[g]})
			}
		}
	}
	return out
}

// allPairs returns rows, each a level for every factor, that together cover
// every pair m requires.
//
// Greedy and deterministic, in the manner of AETG: each row starts from the
// first pair not yet covered, then takes, factor by factor, the valid level
// that covers the most pairs still uncovered, the lowest level on a tie. Not
// the smallest covering array there is, but small, and the same every run,
// so a failing row number means the same command line tomorrow.
func allPairs(m pairModel) [][]int {
	order := m.required()
	uncovered := make(map[pairKey]bool, len(order))
	for _, p := range order {
		uncovered[p] = true
	}

	var rows [][]int
	for len(uncovered) > 0 {
		var seed pairKey
		for _, p := range order {
			if uncovered[p] {
				seed = p
				break
			}
		}
		row := m.unset()
		row[seed.f1], row[seed.f2] = seed.v1, seed.v2
		// Only the seed may carry an invalid level, so a valid row covers
		// valid pairs and an invalid one covers its own.
		bad := -1
		if m.invalid(seed.f1, seed.v1) {
			bad = seed.f1
		} else if m.invalid(seed.f2, seed.v2) {
			bad = seed.f2
		}
		for f := range m.levels {
			if row[f] >= 0 {
				continue
			}
			best, bestScore := -1, -1
			for v := range m.levels[f] {
				row[f] = v
				if m.invalid(f, v) || !m.ok(row) {
					continue
				}
				score := 0
				for g := range m.levels {
					if g != f && row[g] >= 0 && (bad < 0 || g == bad) && uncovered[pairOf(f, v, g, row[g])] {
						score++
					}
				}
				if score > bestScore {
					best, bestScore = v, score
				}
			}
			if best < 0 {
				panic(fmt.Sprintf("allPairs: no valid level of factor %d completes %v", f, row))
			}
			row[f] = best
		}
		for _, p := range m.covers(row) {
			delete(uncovered, p)
		}
		rows = append(rows, row)
	}
	return rows
}

// checkCovers fails unless rows are full rows m accepts, none with two
// invalid levels, and every pair m requires is covered by one of them.
func checkCovers(t *testing.T, m pairModel, rows [][]int) {
	t.Helper()
	seen := map[pairKey]bool{}
	for _, row := range rows {
		bad := 0
		for f, v := range row {
			if v >= 0 && m.invalid(f, v) {
				bad++
			}
		}
		if len(row) != len(m.levels) || slices.Contains(row, -1) || !m.ok(row) || bad > 1 {
			t.Errorf("row %v is not a valid full row", row)
		}
		for _, p := range m.covers(row) {
			seen[p] = true
		}
	}
	for _, p := range m.required() {
		if !seen[p] {
			t.Errorf("pair factor %d=%d, factor %d=%d is in no row", p.f1, p.v1, p.f2, p.v2)
		}
	}
}

func TestAllPairsCoversEveryPair(t *testing.T) {
	none := func(int, int) bool { return false }
	t.Run("unconstrained", func(t *testing.T) {
		m := pairModel{levels: []int{3, 3, 3, 3}, ok: func([]int) bool { return true }, invalid: none}
		rows := allPairs(m)
		checkCovers(t, m, rows)
		// 3^4 = 81 combinations; nine rows is the optimum for four
		// three-level factors, and greedy should land close to it.
		if len(rows) > 15 {
			t.Errorf("%d rows for 3^4; a covering array needs 9", len(rows))
		}
	})
	t.Run("constrained", func(t *testing.T) {
		// Factor 1's level 2 is only possible with factor 0 at level 0.
		m := pairModel{
			levels:  []int{3, 3, 2},
			ok:      func(row []int) bool { return row[1] != 2 || row[0] <= 0 },
			invalid: none,
		}
		checkCovers(t, m, allPairs(m))
	})
	t.Run("invalid levels", func(t *testing.T) {
		// The last level of each factor is an error.
		m := pairModel{
			levels:  []int{3, 3, 3},
			ok:      func([]int) bool { return true },
			invalid: func(_, v int) bool { return v == 2 },
		}
		rows := allPairs(m)
		checkCovers(t, m, rows)
		// The valid pairs alone need 2x2 rows; each invalid level needs
		// its pairs with both valid levels of the two other factors.
		if len(rows) < 4 {
			t.Errorf("%d rows; the valid levels alone need 4", len(rows))
		}
	})
	t.Run("the command-line model", func(t *testing.T) {
		m := cliModel()
		rows := allPairs(m)
		checkCovers(t, m, rows)
		if again := allPairs(m); !slices.EqualFunc(rows, again, slices.Equal[[]int]) {
			t.Error("two runs generated different rows; a row number would not reproduce")
		}
		total := 1
		for _, l := range m.levels {
			total *= l
		}
		t.Logf("%d rows cover every pair of %d combinations", len(rows), total)
	})
}

// ---- the command-line model ----

// cliCommand is what the model knows about one command.
type cliCommand struct {
	name      string
	arg       bool   // takes a positional argument
	multi     bool   // takes several ids
	full      bool   // has -full
	raw       bool   // has -raw
	fields    string // a -fields list naming fields of its records
	rawFields string // the same for -raw, the library's own types; "" when there is no -raw
	id        bool   // its argument is an app id, checked before any request
	objects   bool   // writes JSON objects (suggest and categories write strings)
}

var cliCommands = []cliCommand{
	{name: "app", arg: true, id: true, multi: true, raw: true, fields: "app_id,title", rawFields: "appId,title", objects: true},
	{name: "pcapp", arg: true, id: true, multi: true, fields: "appId,title", objects: true},
	{name: "search", arg: true, full: true, raw: true, fields: "app_id,title", rawFields: "appId,title", objects: true},
	{name: "list", arg: true, full: true, raw: true, fields: "app_id,title", rawFields: "appId,title", objects: true},
	{name: "reviews", arg: true, id: true, raw: true, fields: "id,score", rawFields: "id,score", objects: true},
	{name: "similar", arg: true, id: true, full: true, raw: true, fields: "app_id,title", rawFields: "appId,title", objects: true},
	{name: "developer", arg: true, full: true, raw: true, fields: "app_id,title", rawFields: "appId,title", objects: true},
	{name: "permissions", arg: true, id: true, multi: true, raw: true, fields: "app_id,permissions", rawFields: "appId,permissions", objects: true},
	{name: "datasafety", arg: true, id: true, raw: true, fields: "app_id,collected_data", rawFields: "collectedData", objects: true},
	{name: "suggest", arg: true, fields: "title"},
	{name: "categories", fields: "title"},
	{name: "availability", arg: true, id: true, raw: true, fields: "app_id,checked", rawFields: "appId,checked", objects: true},
}

// The factors, in row order, and their levels.
const (
	fCmd = iota
	fFields
	fFull
	fLang
	fCountry
	fRaw
	fAdaptive
	fInput
	fArity
)

var (
	fieldsLevels   = []string{"none", "valid", "valid+unknown", "unknown"}
	fullLevels     = []string{"default", "true", "false"}
	localeLevels   = []string{"default", "valid", "invalid"}
	switchLevels   = []string{"off", "on"}
	inputLevels    = []string{"valid", "missing", "malformed", "empty"}
	arityLevels    = []string{"one", "two", "repeat"} // the input alone; after a valid id; twice
	invalidLang    = "en,de"
	invalidCountry = "zz!"
)

// cliModel is the factors and their constraints. The invalid levels are the
// ones gpscrape refuses before sending anything: a -fields name no record
// has, a -lang or -country that is not one code, an empty argument, an id
// that is not a package name. A missing id is not among them: it is sent,
// and what the store answers is under test.
func cliModel() pairModel {
	return pairModel{
		levels: []int{
			len(cliCommands), len(fieldsLevels), len(fullLevels), len(localeLevels), len(localeLevels),
			len(switchLevels), len(switchLevels), len(inputLevels), len(arityLevels),
		},
		ok: cliRowOK,
		invalid: func(f, v int) bool {
			switch f {
			case fFields:
				return fieldsLevels[v] == "unknown" || fieldsLevels[v] == "valid+unknown"
			case fLang, fCountry:
				return localeLevels[v] == "invalid"
			case fInput:
				return inputLevels[v] == "empty" || inputLevels[v] == "malformed"
			}
			return false
		},
	}
}

// applicable reports whether factor f can take level v on command c. Level 0
// of every factor is possible everywhere, which is what keeps cliRowOK
// exact: any consistent partial row completes with zeros.
func applicable(c cliCommand, f, v int) bool {
	switch f {
	case fFull:
		return v == 0 || c.full
	case fRaw:
		return v == 0 || c.raw
	case fInput:
		return v == 0 || c.arg
	case fArity:
		return v == 0 || c.multi
	}
	return true
}

func cliRowOK(row []int) bool {
	fits := func(c cliCommand) bool {
		for f, v := range row {
			if f != fCmd && v >= 0 && !applicable(c, f, v) {
				return false
			}
		}
		return true
	}
	if row[fCmd] >= 0 {
		return fits(cliCommands[row[fCmd]])
	}
	return slices.ContainsFunc(cliCommands, fits)
}

// cliRow is one generated row, readable.
type cliRow struct {
	cmd                      cliCommand
	fields, full, lang, ctry string
	raw, adaptive            bool
	input, arity             string
}

func newCLIRow(row []int) cliRow {
	return cliRow{
		cmd: cliCommands[row[fCmd]], fields: fieldsLevels[row[fFields]], full: fullLevels[row[fFull]],
		lang: localeLevels[row[fLang]], ctry: localeLevels[row[fCountry]],
		raw: row[fRaw] == 1, adaptive: row[fAdaptive] == 1,
		input: inputLevels[row[fInput]], arity: arityLevels[row[fArity]],
	}
}

func (r cliRow) String() string {
	return fmt.Sprintf("cmd=%s fields=%s full=%s lang=%s country=%s raw=%v adaptive=%v input=%s arity=%s",
		r.cmd.name, r.fields, r.full, r.lang, r.ctry, r.raw, r.adaptive, r.input, r.arity)
}

func (r cliRow) args() []string {
	args := []string{r.cmd.name}
	if r.cmd.arg {
		in := map[string]string{
			"valid": cliValidID, "missing": cliMissingID, "malformed": cliMalformedID, "empty": "",
		}[r.input]
		switch {
		case r.input == "valid" && (r.cmd.name == "search" || r.cmd.name == "suggest"):
			in = "photo editor"
		case r.input == "valid" && r.cmd.name == "list":
			in = "GAME_PUZZLE"
		case r.input == "valid" && r.cmd.name == "developer":
			in = "Google LLC"
		}
		switch r.arity {
		case "two":
			args = append(args, cliValidID)
		case "repeat":
			args = append(args, in)
		}
		args = append(args, in)
	}
	fields := r.cmd.fields
	if r.raw {
		fields = r.cmd.rawFields
	}
	switch r.fields {
	case "valid":
		args = append(args, "-fields", fields)
	case "valid+unknown":
		args = append(args, "-fields", fields+",no_such_field")
	case "unknown":
		args = append(args, "-fields", "no_such_field")
	}
	if r.full != "default" {
		args = append(args, "-full="+r.full)
	}
	switch r.lang {
	case "valid":
		args = append(args, "-lang", "de")
	case "invalid":
		args = append(args, "-lang", invalidLang)
	}
	switch r.ctry {
	case "valid":
		args = append(args, "-country", "de")
	case "invalid":
		args = append(args, "-country", invalidCountry)
	}
	if r.raw {
		args = append(args, "-raw")
	}
	if r.adaptive {
		args = append(args, "-adaptive")
	}
	if r.cmd.name == "availability" {
		// The default is all 177 storefronts; three say as much.
		args = append(args, "-countries", "us,de,jp")
	}
	return args
}

// ---- the test ----

func TestCLIPairwise(t *testing.T) {
	for i, raw := range allPairs(cliModel()) {
		row := newCLIRow(raw)
		t.Run(fmt.Sprintf("%02d_%s", i, row.cmd.name), func(t *testing.T) {
			t.Parallel()
			t.Logf("row %d: %s\ngpscrape %s", i, row, quoteArgs(row.args()))
			res := runCLI(t, row.args()...)
			fail := func(format string, a ...any) {
				t.Helper()
				t.Errorf("%s\nrow %d: %s\n%s", fmt.Sprintf(format, a...), i, row, res)
			}
			checkCLIInvariants(res, row.cmd.objects, fail)

			// -fields names are checked up front against the shared records;
			// -raw output, and commands with no shared record, are checked
			// against the first record written.
			_, shared := commandRecords[row.cmd.name]
			fieldsChecked := shared && !row.raw && strings.Contains(row.fields, "unknown")
			// suggest and categories write bare strings, so any -fields is
			// refused there.
			fieldsOnStrings := !row.cmd.objects && row.fields != "none"
			invalidArg := row.lang == "invalid" || row.ctry == "invalid" || fieldsChecked || fieldsOnStrings ||
				(row.cmd.arg && row.input == "empty") || (row.cmd.id && row.input == "malformed")
			errs := stderrErrors(res.stderr)
			switch {
			case invalidArg:
				if res.exit != 2 || len(errs) == 0 || errs[len(errs)-1].Code != string(records.CodeInvalid) {
					fail("want exit 2 with invalid_argument: nothing should be sent for this")
				}
			case strings.Contains(row.fields, "unknown"), row.input == "malformed":
				// Exit 2 when a record was there to project, a store answer
				// for a malformed term; the invariants cover both.
			case row.input == "valid":
				if res.exit != 0 {
					fail("want exit 0: every input is good")
				}
			case row.arity == "two":
				// One good id beside one the store does not know: a success,
				// with the failure on its own line.
				var failures int
				for _, rec := range jsonRecords(res.stdout) {
					if _, ok := rec["error"]; ok {
						failures++
					}
				}
				if res.exit != 0 || failures != 1 {
					fail("want exit 0 and one error record, got exit %d and %d", res.exit, failures)
				}
			}
		})
	}
}

// checkCLIInvariants holds for every run, whatever it was asked.
func checkCLIInvariants(res cliResult, objects bool, fail func(string, ...any)) {
	for _, marker := range []string{"panic:", "goroutine ", ".go:", "offlinestore:"} {
		if strings.Contains(res.stderr, marker) {
			fail("stderr carries %q", marker)
		}
	}

	var recs []map[string]any
	for n, line := range strings.Split(strings.TrimRight(res.stdout, "\n"), "\n") {
		if line == "" && res.stdout == "" {
			break
		}
		var v any
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			fail("stdout line %d is not JSON: %v", n+1, err)
			continue
		}
		rec, isObject := v.(map[string]any)
		if objects && !isObject {
			fail("stdout line %d is not a JSON object", n+1)
		}
		if isObject {
			recs = append(recs, rec)
		}
	}

	// Every failure on stdout says what failed and why, -fields or not.
	var failCodes []records.Code
	for _, rec := range recs {
		if _, failed := rec["error"]; !failed {
			continue
		}
		code, _ := rec["code"].(string)
		msg, _ := rec["error"].(string)
		if !knownCode(code) || msg == "" {
			fail("error record %v lacks a known code or a message", rec)
		}
		failCodes = append(failCodes, records.Code(code))
	}

	// The exit status is the code's, and the code is there to read.
	errs := stderrErrors(res.stderr)
	switch {
	case res.exit == 0:
		if len(errs) > 0 {
			fail("exit 0 with an error on stderr")
		}
	case len(errs) != 1:
		fail("exit %d with %d error objects on stderr, want one", res.exit, len(errs))
	default:
		code := records.Code(errs[0].Code)
		if !knownCode(errs[0].Code) || code.ExitCode() != res.exit {
			fail("exit %d for code %q, which maps to %d", res.exit, code, code.ExitCode())
		}
		// Every id failing is a failed run, and its status is the one the
		// records give when they agree.
		if len(failCodes) > 0 && len(failCodes) == len(recs) {
			for _, c := range failCodes {
				if c.ExitCode() != res.exit {
					fail("exit %d, but an error record says %s (exit %d)", res.exit, c, c.ExitCode())
				}
			}
		}
	}
}

func knownCode(c string) bool {
	switch records.Code(c) {
	case records.CodeInvalid, records.CodeNotFound, records.CodeNotListed, records.CodeRateLimited,
		records.CodeTimeout, records.CodeBusy, records.CodeCancelled, records.CodeConfirmation,
		records.CodeStore, records.CodeInternal:
		return true
	}
	return false
}

// jsonRecords is jsonLines without a test to fail: the invariants have
// already reported what does not parse.
func jsonRecords(out string) []map[string]any {
	var recs []map[string]any
	for line := range strings.SplitSeq(out, "\n") {
		var rec map[string]any
		if json.Unmarshal([]byte(line), &rec) == nil && rec != nil {
			recs = append(recs, rec)
		}
	}
	return recs
}

// The failures the pairwise and flag-value runs found, one command line each,
// so that each stays fixed on its own and not only as one row of many.
func TestCLIFoundFailures(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		exit int
		code records.Code // of the stderr error; "" for none
		out  string       // a substring stdout must hold
	}{
		{"a bad flag value is an error object", []string{"search", "x", "-num", "abc"}, 2, records.CodeInvalid, ""},
		{"an unknown flag is an error object", []string{"app", cliValidID, "-nosuch"}, 2, records.CodeInvalid, ""},
		{"-h is not a failure", []string{"app", "-h"}, 0, "", ""},
		{"a negative -num does not panic", []string{"search", "x", "-num", "-1"}, 2, records.CodeInvalid, ""},
		{"a negative -limit is not no limit", []string{"reviews", cliValidID, "-limit", "-1"}, 2, records.CodeInvalid, ""},
		{"an empty id is invalid", []string{"app", ""}, 2, records.CodeInvalid, ""},
		{"an unknown -country is invalid", []string{"app", cliValidID, "-country", "zz!"}, 2, records.CodeInvalid, ""},
		{"-langs all,en is invalid", []string{"reviews", cliValidID, "-langs", "all,en"}, 2, records.CodeInvalid, ""},
		{"-sample out of range is invalid", []string{"catalog", "sweep", "-sample", "-5"}, 2, records.CodeInvalid, ""},
		{"a refused flag is invalid", []string{"catalog", "size", "-exact"}, 2, records.CodeInvalid, ""},
		{"every id failing exits with their code", []string{"permissions", cliMissingID}, 3, records.CodeNotListed, ""},
		{"two ids failing exit with their code", []string{"app", cliMissingID, cliMissingID}, 3, records.CodeNotFound, ""},
		{"every country broken is store_error", []string{"availability", cliBrokenID, "-countries", "us,de,jp"},
			1, records.CodeStore, `"errors":`},
		{"every country refused is rate_limited", []string{"availability", cliLimitedID, "-countries", "us,de,jp"},
			4, records.CodeRateLimited, `"errors":`},
		{"countries failing differently are store_error", []string{"availability", cliFlakyID, "-countries", "us,de,jp"},
			1, records.CodeStore, `"errors":`},
		{"a failure survives -fields", []string{"app", cliValidID, cliMissingID, "-fields", "title"}, 0, "",
			`{"app_id":"` + cliMissingID + `","code":"not_found","error":`},
		{"a raw permissions failure carries its code", []string{"permissions", cliValidID, cliMissingID, "-raw"}, 0, "", `"code":"not_listed"`},
		{"an empty developer is invalid", []string{"developer", " "}, 2, records.CodeInvalid, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			res := runCLI(t, tc.args...)
			errs := stderrErrors(res.stderr)
			var code records.Code
			if len(errs) > 0 {
				code = records.Code(errs[len(errs)-1].Code)
			}
			if res.exit != tc.exit || code != tc.code || !strings.Contains(res.stdout, tc.out) {
				t.Errorf("want exit %d, code %q, stdout holding %q\n%s", tc.exit, tc.code, tc.out, res)
			}
			checkCLIInvariants(res, true, func(format string, a ...any) {
				t.Errorf("%s\n%s", fmt.Sprintf(format, a...), res)
			})
		})
	}
}

// suggest and categories print one JSON string per line: there is nothing
// for -fields to keep, and it used to be ignored without a word.
func TestFieldsOnStringCommandsIsRefused(t *testing.T) {
	for _, args := range [][]string{
		{"suggest", "photo", "-fields", "title"},
		{"categories", "-fields", "title"},
	} {
		res := runCLI(t, args...)
		errs := stderrErrors(res.stderr)
		if res.exit != 2 || len(errs) == 0 || errs[len(errs)-1].Code != string(records.CodeInvalid) {
			t.Errorf("%v: exit %d, stderr %s; want exit 2 with invalid_argument", args, res.exit, res.stderr)
		}
		if res.stdout != "" {
			t.Errorf("%v wrote %q; a refused command writes nothing", args, res.stdout)
		}
	}
}
