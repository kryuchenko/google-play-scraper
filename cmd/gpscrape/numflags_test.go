package main

import (
	"flag"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kryuchenko/google-play-scraper/records"
)

// The integer-field checklist ("Lessons Learned in Software Testing"), run on
// every numeric flag of every command: zero, one, minus one, the largest
// plausible value and one past it, an overflow, and the spellings a shell or a
// script produces by accident. Each value is either accepted or refused with
// invalid_argument and exit 2, the status a bad flag has always had.

// numRule is what a numeric flag accepts. A flag found on a command with no
// rule here fails the test: a new flag is decided on, not skipped.
type numRule struct {
	min, max  float64 // the accepted range; NaN for no bound
	plausible float64 // the largest value a caller would mean
}

var noBound = math.NaN()

var numRules = map[string]numRule{
	// Clamped above (50 for search, 660 for list), never refused there.
	"num":         {min: 0, max: noBound, plausible: 50},     // 0 = the default
	"limit":       {min: 0, max: noBound, plausible: 100000}, // 0 = no limit
	"score":       {min: 0, max: 5, plausible: 5},            // 0 = every rating
	"concurrency": {min: 0, max: noBound, plausible: 64},     // 0 is one at a time
	"pilot":       {min: 0, max: noBound, plausible: 83445},  // 0 = the default
	"keep":        {min: 0, max: noBound, plausible: 100},    // 0 = keep every generation
	"seed":        {min: noBound, max: noBound, plausible: 1 << 62},
	"sample":      {min: 0, max: 100, plausible: 100}, // a percent; 0 = the whole catalog
	// Durations: 0 means no throttle and no deadline, on purpose.
	"throttle": {min: 0, max: noBound},
	"timeout":  {min: 0, max: noBound},
	// Only with -adaptive, and no higher than -throttle: adaptiveMinAccepted.
	"adaptive-min": {min: 0, max: noBound},
}

// numericFlags lists every numeric flag of every command, read from the flag
// sets the commands register, the way `gpscrape schema` reads them.
//
// adaptive reports, per command, whether -adaptive is on by default.
func numericFlags(t *testing.T) (flags map[string][]*flag.Flag, adaptive map[string]bool) {
	t.Helper()
	names := map[string]commandFunc{}
	for n, run := range commands {
		if n != "schema" && n != "mcp" && n != "catalog" {
			names[n] = run
		}
	}
	for v, run := range catalogVerbs {
		names["catalog "+v] = run
	}
	out := map[string][]*flag.Flag{}
	adaptive = map[string]bool{}
	for name, run := range names {
		describing, described = true, nil
		err := run(nil)
		describing = false
		if described == nil {
			t.Fatalf("%s: no flag set captured (%v)", name, err)
		}
		described.VisitAll(func(f *flag.Flag) {
			if f.Name == "adaptive" {
				adaptive[name] = f.DefValue == "true"
			}
			switch f.Value.(flag.Getter).Get().(type) {
			case int, int64, uint64, float64, time.Duration:
				out[name] = append(out[name], f)
			}
		})
	}
	return out, adaptive
}

// adaptiveMinAccepted decides -adaptive-min, which the checklist passes
// alone. It lowers the floor of -adaptive below -throttle and does nothing
// else, so a value it could not act on is refused rather than ignored: any
// positive value without -adaptive, and one above -throttle's default 200ms.
// 0 is the default and always accepted.
func adaptiveMinAccepted(raw string, adaptiveOn bool) bool {
	d, err := time.ParseDuration(raw)
	if err != nil || d < 0 {
		return false
	}
	return d == 0 || (adaptiveOn && d <= 200*time.Millisecond)
}

// positional is the argument a command cannot run without.
func positional(cmd string) []string {
	switch cmd {
	case "app", "pcapp", "reviews", "similar", "permissions", "datasafety":
		return []string{cliValidID}
	case "availability":
		return []string{cliValidID, "-countries", "us"}
	case "search", "suggest":
		return []string{"photo editor"}
	case "developer":
		return []string{"Google LLC"}
	}
	return nil
}

// wantAccepted decides one value. Syntax is the flag package's: integers in
// strconv base 0, so "+5" is 5 and "05" is 5 (octal; "010" would be 8),
// while " 5", "1.5", "5e2" and "" do not parse. Floats take "5e2" and "NaN",
// and NaN is refused like any value out of range.
func wantAccepted(f *flag.Flag, r numRule, raw string) bool {
	var v float64
	switch f.Value.(flag.Getter).Get().(type) {
	case int, int64:
		n, err := strconv.ParseInt(raw, 0, 64)
		if err != nil {
			return false
		}
		v = float64(n)
	case uint64:
		n, err := strconv.ParseUint(raw, 0, 64)
		if err != nil {
			return false
		}
		v = float64(n)
	case float64:
		n, err := strconv.ParseFloat(raw, 64)
		if err != nil || math.IsNaN(n) {
			return false
		}
		v = n
	case time.Duration:
		d, err := time.ParseDuration(raw)
		if err != nil {
			return false
		}
		v = float64(d)
	}
	return (math.IsNaN(r.min) || v >= r.min) && (math.IsNaN(r.max) || v <= r.max)
}

func checklist(f *flag.Flag, r numRule) []string {
	if _, ok := f.Value.(flag.Getter).Get().(time.Duration); ok {
		return []string{"0s", "-1ms", "1ns", "1h", "garbage", ""}
	}
	vals := []string{"0", "1", "-1", "99999999999999999999", "+5", " 5", "05", "1.5", "5e2", ""}
	if !math.IsNaN(r.plausible) {
		vals = append(vals, strconv.FormatFloat(r.plausible, 'f', -1, 64),
			strconv.FormatFloat(r.plausible+1, 'f', -1, 64))
	}
	if _, ok := f.Value.(flag.Getter).Get().(float64); ok {
		vals = append(vals, "NaN", "garbage")
	}
	return vals
}

// The decisions spelled out, so that the table above is checked against
// something other than itself.
func TestNumericFlagDecisions(t *testing.T) {
	num := &flag.Flag{Value: new(intFlagValue)}
	for raw, want := range map[string]bool{
		"0": true, "1": true, "-1": false, "50": true, "51": true, "99999999999999999999": false,
		"+5": true, " 5": false, "05": true, "1.5": false, "5e2": false, "": false,
	} {
		if got := wantAccepted(num, numRules["num"], raw); got != want {
			t.Errorf("-num %q: accepted = %v, want %v", raw, got, want)
		}
	}
	score := numRules["score"]
	if wantAccepted(num, score, "6") || !wantAccepted(num, score, "0") {
		t.Error("-score: want 0 to 5")
	}
}

// intFlagValue is a stand-in int flag for TestNumericFlagDecisions.
type intFlagValue int

func (v *intFlagValue) String() string     { return fmt.Sprint(int(*v)) }
func (v *intFlagValue) Set(s string) error { return nil }
func (v *intFlagValue) Get() any           { return int(*v) }

func TestNumericFlagChecklist(t *testing.T) {
	flags, adaptiveOn := numericFlags(t)
	// The flags every command shares are checked by one piece of code; with
	// -short they are run on one command, not on all twenty. A process
	// each costs most under -race.
	shared := newCommon("x").fs
	sharedDone := map[string]bool{}
	for _, cmd := range slices.Sorted(func(yield func(string) bool) {
		for k := range flags {
			if !yield(k) {
				return
			}
		}
	}) {
		for _, f := range flags[cmd] {
			r, ok := numRules[f.Name]
			if !ok {
				t.Errorf("%s -%s is a numeric flag with no rule in numRules: decide what it accepts", cmd, f.Name)
				continue
			}
			if shared.Lookup(f.Name) != nil && testing.Short() {
				if sharedDone[f.Name] {
					continue
				}
				sharedDone[f.Name] = true
			}
			for _, raw := range checklist(f, r) {
				want := wantAccepted(f, r, raw)
				if f.Name == "adaptive-min" {
					want = adaptiveMinAccepted(raw, adaptiveOn[cmd])
				}
				t.Run(fmt.Sprintf("%s/-%s=%q", cmd, f.Name, raw), func(t *testing.T) {
					t.Parallel()
					args := append(strings.Fields(cmd), positional(cmd)...)
					res := runCLI(t, append(args, "-"+f.Name+"="+raw)...)
					fail := func(format string, a ...any) {
						t.Helper()
						t.Errorf("%s\n%s", fmt.Sprintf(format, a...), res)
					}
					for _, marker := range []string{"panic:", "goroutine ", ".go:"} {
						if strings.Contains(res.stderr, marker) {
							fail("stderr carries %q", marker)
						}
					}
					errs := stderrErrors(res.stderr)
					if res.exit != 0 && (len(errs) != 1 || records.Code(errs[0].Code).ExitCode() != res.exit) {
						fail("exit %d without one error object whose code maps to it", res.exit)
					}
					refused := res.exit == 2 && len(errs) == 1 && errs[0].Code == string(records.CodeInvalid) &&
						strings.Contains(errs[0].Message, "-"+f.Name)
					switch {
					case want && refused:
						fail("-%s=%q is accepted by the rule, but was refused", f.Name, raw)
					case !want && !refused:
						fail("-%s=%q should be refused with invalid_argument naming the flag", f.Name, raw)
					}
				})
			}
		}
	}
}

// -adaptive-min beside -adaptive and -throttle, the combinations the
// checklist does not reach by passing one flag.
func TestAdaptiveMin(t *testing.T) {
	for _, tc := range []struct {
		args []string
		ok   bool
	}{
		{[]string{"-adaptive", "-adaptive-min", "50ms"}, true},                      // below the default -throttle
		{[]string{"-adaptive", "-throttle", "1s", "-adaptive-min", "500ms"}, true},  // below -throttle
		{[]string{"-adaptive", "-throttle", "1s", "-adaptive-min", "1s"}, true},     // equal: no faster, no harm
		{[]string{"-adaptive", "-throttle", "100ms", "-adaptive-min", "1s"}, false}, // above -throttle
		{[]string{"-adaptive-min", "50ms"}, false},                                  // without -adaptive
		{[]string{"-adaptive=false", "-adaptive-min", "50ms"}, false},
		{[]string{"-adaptive", "-adaptive-min", "-1ms"}, false},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			t.Parallel()
			res := runCLI(t, append([]string{"app", cliValidID}, tc.args...)...)
			errs := stderrErrors(res.stderr)
			refused := res.exit == 2 && len(errs) == 1 && errs[0].Code == string(records.CodeInvalid) &&
				strings.Contains(errs[0].Message, "-adaptive-min")
			if tc.ok && res.exit != 0 || !tc.ok && !refused {
				t.Errorf("want accepted = %v\n%s", tc.ok, res)
			}
		})
	}
	// catalog sweep is adaptive by default, so -adaptive-min alone is
	// accepted there, and refused after -adaptive=false.
	if res := runCLI(t, "catalog", "sweep", "-adaptive-min", "50ms"); strings.Contains(res.stderr, "-adaptive-min") {
		t.Errorf("catalog sweep refused -adaptive-min although it is adaptive by default\n%s", res)
	}
	if res := runCLI(t, "catalog", "sweep", "-adaptive=false", "-adaptive-min", "50ms"); res.exit != 2 {
		t.Errorf("catalog sweep -adaptive=false took -adaptive-min\n%s", res)
	}
}

// -adaptive takes -throttle as its floor under a fixed 2s ceiling. A floor
// above the ceiling is accepted: the library raises the ceiling to the floor,
// so the run goes no faster than asked.
func TestAdaptiveThrottleAboveTheCeiling(t *testing.T) {
	res := runCLI(t, "app", cliValidID, "-adaptive", "-throttle", "5s")
	if res.exit != 0 {
		t.Errorf("want exit 0\n%s", res)
	}
}
