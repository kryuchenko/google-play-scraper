package googleplayscraper

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"math"
	"math/rand/v2"
	"net/http"
	"os"
	"slices"
	"strconv"
	"testing"
	"testing/synctest"
	"time"
)

// State-model tests for the adaptive throttle and the breaker.
//
// The hand-written tests in adaptive_test.go and breaker_test.go each check a
// path someone thought of. These feed long random sequences -- ten thousand
// events per run -- and check the controller's state after every one of them,
// so that a violation is caught at the event that caused it rather than as a
// wrong number at the end.
//
// Generation and execution are separate. genEvents turns a seed into a plan
// (responses, refusals, idle gaps); the executor plays the plan against a
// client inside a synctest bubble, whose clock is virtual: pauses of an hour
// cost nothing, and admit's waits run exactly as in production. The only
// thing the seed does not fix is the breaker's pause jitter, which comes from
// the global math/rand source; a replay repeats the plan exactly and the
// controller's path up to that jitter.
//
// A failure names the seed and the event index. Replay one with
//
//	go test -run 'TestAdaptiveModel' -adaptive.seed=N .
//
// or ADAPTIVE_SEED=N in the environment.

var adaptiveSeedFlag = flag.Uint64("adaptive.seed", 0, "replay one seed of the adaptive state-model tests")

const modelEvents = 10_000

// modelSeeds is the list of seeds to run: one when replaying, a handful in
// -short mode, many otherwise. The seeds are fixed so a run is repeatable.
func modelSeeds(t *testing.T) []uint64 {
	t.Helper()
	if s := *adaptiveSeedFlag; s != 0 {
		return []uint64{s}
	}
	if v := os.Getenv("ADAPTIVE_SEED"); v != "" {
		s, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			t.Fatalf("ADAPTIVE_SEED=%q: %v", v, err)
		}
		return []uint64{s}
	}
	n := 64
	if testing.Short() {
		n = 3
	}
	seeds := make([]uint64, n)
	for i := range seeds {
		seeds[i] = uint64(i+1) * 0x9e3779b97f4a7c15
	}
	return seeds
}

// --- generation ---------------------------------------------------------

type regime uint8

const (
	regimeClean    regime = iota // 200s with ordinary latency
	regimeSporadic               // mostly 200, the odd 429/503/500/transport error
	regimeSlow                   // a real slowdown: every response several times slower
	regimeOutage                 // the server refuses nearly everything
	regimeCeiling                // the server refuses whenever requests come faster than X
	regimeMixed                  // phases of the first four, of random length
)

func (r regime) String() string {
	return [...]string{"clean", "sporadic", "slow", "outage", "ceiling", "mixed"}[r]
}

type evKind uint8

const (
	evResponse evKind = iota
	evIdle            // the caller sends nothing for idle
)

// modelEvent is one step of a plan. Comparable, so plans compare with ==.
type modelEvent struct {
	kind       evKind
	regime     regime
	status     int // 200, 429, 503, 500; 0 is a transport error
	rtt        time.Duration
	retryAfter time.Duration
	// ceiling, when set, overrides status: the server answers 429 if this
	// request started less than ceiling after the previous one.
	ceiling time.Duration
	// pair admits this request together with the next one, so the second is
	// in flight when the first one's answer lands.
	pair bool
	idle time.Duration
}

func (e modelEvent) String() string {
	if e.kind == evIdle {
		return fmt.Sprintf("idle %v", e.idle)
	}
	s := fmt.Sprintf("%s status=%d rtt=%v", e.regime, e.status, e.rtt)
	if e.retryAfter > 0 {
		s += fmt.Sprintf(" retry-after=%v", e.retryAfter)
	}
	if e.ceiling > 0 {
		s += fmt.Sprintf(" ceiling=%v", e.ceiling)
	}
	if e.pair {
		s += " pair"
	}
	return s
}

func durMS(ms float64) time.Duration {
	return max(time.Millisecond, time.Duration(ms*float64(time.Millisecond)))
}

// sampleRTT draws a latency shaped like Google's CDN as measured: one cache
// hit in twelve around 22ms, the body log-normal around 48ms, a thin tail.
func sampleRTT(r *rand.Rand) time.Duration {
	switch u := r.Float64(); {
	case u < 1.0/12:
		return durMS(22 + 1.5*r.NormFloat64())
	case u < 0.97:
		return durMS(48 * math.Exp(0.12*r.NormFloat64()))
	default:
		return durMS(70 + 20*r.ExpFloat64())
	}
}

// sampleIdle draws an idle gap log-uniform between one second and two
// hours: past every Cooldown the executor uses, and past CutMemory often.
func sampleIdle(r *rand.Rand) time.Duration {
	lo, hi := math.Log(float64(time.Second)), math.Log(float64(2*time.Hour))
	return time.Duration(math.Exp(lo + r.Float64()*(hi-lo)))
}

func sampleRetryAfter(r *rand.Rand) time.Duration {
	if r.IntN(20) == 0 {
		return 2 * time.Hour // past any MaxCooldown: stops the breaker
	}
	return time.Duration(1+r.IntN(10)) * time.Second
}

func refusal(r *rand.Rand, e modelEvent) modelEvent {
	e.status = http.StatusTooManyRequests
	if r.IntN(3) == 0 {
		e.status = http.StatusServiceUnavailable
	}
	if r.IntN(2) == 0 {
		e.retryAfter = sampleRetryAfter(r)
	}
	return e
}

// genOne draws one event of regime g.
func genOne(r *rand.Rand, g regime, ceiling time.Duration) modelEvent {
	e := modelEvent{kind: evResponse, regime: g, status: http.StatusOK, rtt: sampleRTT(r)}
	if g == regimeCeiling {
		e.ceiling = ceiling
		return e
	}
	if r.Float64() < 0.003 {
		return modelEvent{kind: evIdle, regime: g, idle: sampleIdle(r)}
	}
	e.pair = r.Float64() < 0.05
	switch g {
	case regimeSporadic:
		switch u := r.Float64(); {
		case u < 0.03:
			e = refusal(r, e)
		case u < 0.04:
			e.status = http.StatusInternalServerError
		case u < 0.045:
			e.status = 0
		}
	case regimeSlow:
		e.rtt = time.Duration(float64(e.rtt) * (3 + 3*r.Float64()))
	case regimeOutage:
		switch u := r.Float64(); {
		case u < 0.85:
			e = refusal(r, e)
		case u < 0.9:
			e.status = http.StatusInternalServerError
		default:
			e.rtt *= 3
		}
	}
	return e
}

// genEvents is the plan for one run: n events of regime g, or for
// regimeMixed phases of 200 to 1500 events of the other regimes. ceiling is
// used by regimeCeiling only. It draws only from a PCG seeded with seed, so
// the same seed always gives the same plan.
func genEvents(seed uint64, n int, g regime, ceiling time.Duration) []modelEvent {
	r := rand.New(rand.NewPCG(seed, 0x5eed))
	out := make([]modelEvent, 0, n)
	phase, left := g, n
	if g == regimeMixed {
		left = 0 // draw the first phase
	}
	for len(out) < n {
		if g == regimeMixed && left <= 0 {
			// Weighted: clean 35%, sporadic 35%, slow 15%, outage 15%.
			switch u := r.Float64(); {
			case u < 0.35:
				phase = regimeClean
			case u < 0.70:
				phase = regimeSporadic
			case u < 0.85:
				phase = regimeSlow
			default:
				phase = regimeOutage
			}
			left = 200 + r.IntN(1300)
		}
		out = append(out, genOne(r, phase, ceiling))
		left--
	}
	return out
}

// --- the reference model ------------------------------------------------

// refModel is the executor's own account of what the controller should
// know: the latency window, the run counters, the remembered cuts. It is
// written from AdaptivePolicy's documentation, not from adjust.
type refModel struct {
	p     AdaptivePolicy
	start time.Duration // the caller's number, 0 if they gave none

	rtts     [32]time.Duration
	nrtt     int
	clean    int
	slow     int
	cuts     []cut // the last five cuts
	refusing bool  // a refusal since the last answer
}

func (m *refModel) baseline() time.Duration {
	var filled []time.Duration
	for _, d := range m.rtts {
		if d > 0 {
			filled = append(filled, d)
		}
	}
	if len(filled) < len(m.rtts)/2 {
		return 0
	}
	slices.Sort(filled)
	return filled[len(filled)/2]
}

func (m *refModel) floor(now time.Time) time.Duration {
	var live []time.Duration
	for _, k := range m.cuts {
		if now.Sub(k.at) < m.p.CutMemory {
			live = append(live, k.interval)
		}
	}
	if len(live) == 0 {
		return 0
	}
	slices.Sort(live)
	return time.Duration(float64(live[len(live)/2]) * (1 + m.p.Headroom))
}

type stepKind uint8

const (
	stepRefusal stepKind = iota
	stepSoft
	stepSlow       // slow, streak not complete
	stepSlowStreak // slow, streak complete: slows down
	stepClean      // clean, window not complete
	stepWindow     // clean, window complete: may speed up
	stepIgnored    // a refusal admitted before the current pause: no effect
)

// classify applies one outcome to the model and says what the controller
// is entitled to do with it. before is the interval it arrived at, st the
// breaker's state, stale whether the request was admitted before the
// breaker's current pause.
//
// A cut is the first 429 after an answer, below Max, with the breaker
// closed: the interval a working rate began to fail at. Later refusals in
// the same episode, refusals at Max, probes and 503s say the server is
// refusing, not where its limit is.
func (m *refModel) classify(o outcome, before time.Duration, now time.Time, st BreakerState, stale bool) stepKind {
	if o.err == nil && o.status == http.StatusOK && o.rtt > 0 {
		m.rtts[m.nrtt%len(m.rtts)] = o.rtt
		m.nrtt++
	}
	switch {
	case o.status == http.StatusTooManyRequests || o.status == http.StatusServiceUnavailable:
		if stale {
			return stepIgnored
		}
		m.clean, m.slow = 0, 0
		if o.status == http.StatusTooManyRequests && !m.refusing && before < m.p.Max && st == BreakerClosed {
			m.cuts = append(m.cuts, cut{interval: before, at: now})
			if len(m.cuts) > 5 {
				m.cuts = m.cuts[1:]
			}
		}
		m.refusing = true
		return stepRefusal
	case o.err != nil || o.status >= 500:
		m.clean, m.slow = 0, 0
		return stepSoft
	}
	m.refusing = false
	if b := m.baseline(); b > 0 && o.rtt > time.Duration(float64(b)*m.p.LatencyRatio) {
		// One slow response leaves the clean run alone; a streak ends it.
		m.slow++
		if m.slow >= m.p.SlowStreak {
			m.clean, m.slow = 0, 0
			return stepSlowStreak
		}
		return stepSlow
	}
	m.slow = 0
	m.clean++
	window := m.p.Window
	if m.start > 0 && before <= m.start {
		window = m.p.ProbeWindow
	}
	if m.clean < window {
		return stepClean
	}
	m.clean = 0
	return stepWindow
}

// --- execution ----------------------------------------------------------

type firedEvent struct {
	ev BreakerEvent
	at time.Time
}

type modelRun struct {
	t     *testing.T
	seed  uint64
	plan  []modelEvent
	desc  string
	c     *Client
	m     refModel
	fired []firedEvent
	seen  int // fired events already checked

	i          int // index of the event being executed
	lastStart  time.Time
	pauseUntil time.Time
	samples    []time.Duration // interval at each admitted request, by event
	sampleIdx  []int
	refusedAt  []int // event indices of refusals
}

func (r *modelRun) fail(format string, args ...any) {
	r.t.Helper()
	ev := "-"
	if r.i < len(r.plan) {
		ev = r.plan[r.i].String()
	}
	r.t.Fatalf("seed=%d event=%d (%s)\n  client: %s\n  %s\n  replay: go test -run 'TestAdaptiveModel' -adaptive.seed=%d .",
		r.seed, r.i, ev, r.desc, fmt.Sprintf(format, args...), r.seed)
}

// startOption is how the caller told the client where to begin.
type startOption uint8

const (
	startNone        startOption = iota // nothing: begins at Max
	startField                          // AdaptivePolicy.Start
	startBefore                         // WithThrottle before WithAdaptiveThrottle
	startAfter                          // WithThrottle after WithAdaptiveThrottle
	startFieldBefore                    // Start, and a different WithThrottle before
	startFieldAfter                     // Start, and a different WithThrottle after
)

func pick[T any](r *rand.Rand, xs ...T) T { return xs[r.IntN(len(xs))] }

// newModelRun builds a client from a policy drawn from the seed. ceiling > 0
// is the hard-ceiling run, which needs a policy that can settle (see
// TestAdaptiveModelHardCeiling).
func newModelRun(t *testing.T, seed uint64, plan []modelEvent, ceiling time.Duration) *modelRun {
	r := rand.New(rand.NewPCG(seed, 0xc0f1))
	p := AdaptivePolicy{
		Min:         pick(r, 10*time.Millisecond, 20*time.Millisecond, 50*time.Millisecond),
		Max:         pick(r, time.Second, 2*time.Second, 5*time.Second),
		Window:      pick(r, 0, 5, 10, 20),
		ProbeWindow: pick(r, 0, 15, 40),
		CutMemory:   pick(r, 0, 30*time.Second, 5*time.Minute),
		Headroom:    pick(r, 0, 0.1, 0.5),
		Cooldown:    pick(r, 0, 0, time.Second, 5*time.Second),
		MaxCooldown: pick[time.Duration](r, 0, 20*time.Second),
		MaxProbes:   pick(r, 0, 2, -1),
	}
	how := pick(r, startNone, startField, startBefore, startAfter, startFieldBefore, startFieldAfter)
	start := time.Duration(r.Int64N(int64(3*time.Second))) + 5*time.Millisecond
	if ceiling > 0 {
		// Settling above the ceiling needs (1+Headroom)*Speedup >= 1 and a
		// CutMemory that spans many probe cycles; see the band derivation.
		p.Min, p.Max = 20*time.Millisecond, 2*time.Second
		p.Headroom = pick(r, 0, 0.5)
		p.CutMemory = pick(r, 0, time.Hour)
		p.Window = pick(r, 0, 10)
		p.ProbeWindow = 0
		start = pick(r, ceiling/2, 2*ceiling, 4*ceiling)
	}
	other := start/2 + time.Millisecond
	if how == startField || how == startFieldBefore || how == startFieldAfter {
		p.Start = start
	}

	run := &modelRun{t: t, seed: seed, plan: plan}
	hooks := WithHooks(Hooks{OnBreaker: func(ev BreakerEvent) {
		run.fired = append(run.fired, firedEvent{ev: ev, at: time.Now()})
	}})
	var opts []ClientOption
	switch how {
	case startBefore:
		opts = []ClientOption{WithThrottle(start), WithAdaptiveThrottle(p), hooks}
	case startAfter:
		opts = []ClientOption{WithAdaptiveThrottle(p), WithThrottle(start), hooks}
	case startFieldBefore:
		opts = []ClientOption{WithThrottle(other), WithAdaptiveThrottle(p), hooks}
	case startFieldAfter:
		opts = []ClientOption{WithAdaptiveThrottle(p), WithThrottle(other), hooks}
	default:
		opts = []ClientOption{WithAdaptiveThrottle(p), hooks}
	}
	run.c = NewClient(opts...)
	// The normalised policy: defaults are not what is under test here.
	run.m.p = *run.c.adaptive
	if how != startNone {
		run.m.start = run.c.CurrentThrottle()
	}
	// Start wins over WithThrottle in either order; WithThrottle alone is
	// the start, clamped; with neither the client begins at Max.
	want := run.m.p.Max
	switch how {
	case startField, startFieldBefore, startFieldAfter:
		want = run.m.p.Start
	case startBefore, startAfter:
		want = min(max(start, run.m.p.Min), run.m.p.Max)
	}
	if got := run.c.CurrentThrottle(); got != want {
		t.Fatalf("seed=%d: start option %d began at %v, want %v", seed, how, got, want)
	}
	run.desc = fmt.Sprintf("start=%d(%v) policy=%+v", how, run.m.start, run.m.p)
	return run
}

func (r *modelRun) snapshot() (thr, floor time.Duration, st BreakerState, until time.Time) {
	r.c.throttleLock.Lock()
	defer r.c.throttleLock.Unlock()
	return r.c.throttle, r.c.cutFloor(), r.c.brk.state, r.c.brk.until
}

// legal is the breaker's transition relation. Closed goes to open, or to
// stopped on a Retry-After past MaxCooldown; a pause ends in half-open, or an
// open breaker is stopped by such a Retry-After; a probe closes, reopens or
// stops it. Nothing goes back to closed except a probe, and nothing moves to
// the state it is in.
var legal = map[[2]BreakerState]bool{
	{BreakerClosed, BreakerOpen}:      true,
	{BreakerClosed, BreakerStopped}:   true,
	{BreakerOpen, BreakerHalfOpen}:    true,
	{BreakerOpen, BreakerStopped}:     true,
	{BreakerStopped, BreakerHalfOpen}: true,
	{BreakerHalfOpen, BreakerClosed}:  true,
	{BreakerHalfOpen, BreakerOpen}:    true,
	{BreakerHalfOpen, BreakerStopped}: true,
}

// checkState is the part of the invariant that holds after every step,
// whatever the step was.
func (r *modelRun) checkState() {
	r.t.Helper()
	p := r.m.p
	thr, floor, st, until := r.snapshot()
	if thr < p.Min || thr > p.Max || thr <= 0 {
		r.fail("interval %v outside [%v, %v]", thr, p.Min, p.Max)
	}
	if want := r.m.floor(time.Now()); floor != want {
		r.fail("cut floor %v, model says %v from cuts %v (CutMemory %v)", floor, want, r.m.cuts, p.CutMemory)
	}

	prev := BreakerClosed
	if r.seen > 0 {
		prev = r.fired[r.seen-1].ev.To
	}
	for ; r.seen < len(r.fired); r.seen++ {
		f := r.fired[r.seen]
		if f.ev.From != prev {
			r.fail("breaker event %s->%s, but it was %s", f.ev.From, f.ev.To, prev)
		}
		if !legal[[2]BreakerState{f.ev.From, f.ev.To}] {
			r.fail("illegal breaker transition %s->%s (%s)", f.ev.From, f.ev.To, f.ev.Reason)
		}
		if (f.ev.To == BreakerOpen || f.ev.To == BreakerStopped) && !f.ev.Until.After(f.at) {
			r.fail("breaker %s with a pause ending %v, not after now %v", f.ev.To, f.ev.Until, f.at)
		}
		if f.ev.Reason == "probes" && (p.MaxProbes <= 0 || f.ev.FailedProbes < p.MaxProbes) {
			r.fail("stopped for probes after %d failed probes, MaxProbes %d", f.ev.FailedProbes, p.MaxProbes)
		}
		prev = f.ev.To
	}
	if st != prev {
		r.fail("breaker reports %s, its last event said %s", st, prev)
	}
	if !r.c.breakerOn() && st != BreakerClosed {
		r.fail("breaker %s without a Cooldown", st)
	}
	if st == BreakerOpen || st == BreakerStopped {
		r.pauseUntil = until
	}
}

// admitOne admits a request the way the pipeline does and checks that the
// breaker let it through only when it should have. A nil ticket means the
// client is stopped; the executor then waits the stop out.
//
// It also returns how many breaker events had fired at admission: a ticket
// is stale -- admitted before the current pause -- once any has fired since,
// because every transition but a probe's close begins a new pause or probe,
// and a probe is never in flight with another request here.
func (r *modelRun) admitOne() (*ticket, int) {
	r.t.Helper()
	t, waited, err := r.c.admit(context.Background())
	now := time.Now()
	if waited < 0 {
		r.fail("admit waited %v", waited)
	}
	thr, _, st, until := r.snapshot()
	if err != nil {
		if !errors.Is(err, ErrBlocked) || st != BreakerStopped {
			r.fail("admit failed with %v in state %s", err, st)
		}
		if !now.Before(until) {
			r.fail("stopped breaker refused at %v, its stop ended %v", now, until)
		}
		time.Sleep(until.Sub(now))
		r.checkState()
		return nil, 0
	}
	if now.Before(r.pauseUntil) {
		r.fail("request admitted at %v, inside a pause until %v", now, r.pauseUntil)
	}
	switch {
	case t.probe && st != BreakerHalfOpen:
		r.fail("probe admitted with the breaker %s", st)
	case !t.probe && st != BreakerClosed:
		r.fail("request admitted with the breaker %s", st)
	}
	if !r.lastStart.IsZero() && !t.probe && now.Sub(r.lastStart) < thr && st == BreakerClosed {
		// The throttle paces starts; a probe is the one exception, sent the
		// moment the pause ends.
		r.fail("request started %v after the last, interval is %v", now.Sub(r.lastStart), thr)
	}
	r.samples = append(r.samples, thr)
	r.sampleIdx = append(r.sampleIdx, r.i)
	r.checkState()
	return t, len(r.fired)
}

// outcomeOf is what the pipeline would hand record for event e, sent gap
// after the previous request.
func outcomeOf(e modelEvent, gap time.Duration) outcome {
	status, ra := e.status, e.retryAfter
	if e.ceiling > 0 {
		status, ra = http.StatusOK, 0
		if gap < e.ceiling {
			status = http.StatusTooManyRequests
		}
	}
	switch status {
	case 0:
		return outcome{rtt: e.rtt, err: errors.New("connection reset")}
	case http.StatusOK:
		return outcome{status: status, rtt: e.rtt}
	}
	return outcome{status: status, rtt: e.rtt, retryAfter: ra, cause: &StatusError{Code: status, RetryAfter: ra}}
}

// resolve applies one outcome and checks the interval against the model.
func (r *modelRun) resolve(t *ticket, firedAt int, o outcome) {
	r.t.Helper()
	p := r.m.p
	before, _, st, _ := r.snapshot()
	now := time.Now()
	stale := r.c.breakerOn() && len(r.fired) > firedAt
	clean, slow := r.m.clean, r.m.slow
	kind := r.m.classify(o, before, now, st, stale)
	floor := r.m.floor(now)
	if kind == stepRefusal {
		r.refusedAt = append(r.refusedAt, r.i)
	}

	ctx := context.Background()
	r.c.record(ctx, t, o)
	r.c.release(t)
	if o.cause != nil && !r.c.breakerOn() {
		r.c.retryAfterHint(ctx, o.cause) // as the pipeline does
	}
	after, _, _, _ := r.snapshot()

	switch kind {
	case stepIgnored:
		if after != before {
			r.fail("a refusal sent before the pause moved the interval %v -> %v", before, after)
		}
		r.c.throttleLock.Lock()
		cr, sr := r.c.cleanRun, r.c.slowRun
		r.c.throttleLock.Unlock()
		if cr != clean || sr != slow {
			r.fail("a refusal sent before the pause reset the runs: clean %d -> %d, slow %d -> %d", clean, cr, slow, sr)
		}
	case stepRefusal, stepSoft, stepSlowStreak:
		if after < before {
			r.fail("step %d shortened the interval %v -> %v", kind, before, after)
		}
		if after == before && before < p.Max && kind != stepSlowStreak {
			r.fail("step %d left the interval at %v below Max %v", kind, before, p.Max)
		}
	case stepSlow, stepClean:
		if after != before {
			r.fail("interval moved %v -> %v without a full window (clean run %d, slow run %d)",
				before, after, r.m.clean, r.m.slow)
		}
	case stepWindow:
		if after > before {
			r.fail("a clean window lengthened the interval %v -> %v (cut floor %v)", before, after, floor)
		}
		if floor > 0 && after < min(before, floor) {
			r.fail("speed-up to %v crossed the cut floor %v", after, floor)
		}
		if before > max(p.Min, floor) && after >= before {
			r.fail("a full clean window at %v (floor %v, Min %v) did not speed up", before, floor, p.Min)
		}
	}
	r.checkState()
}

// execute plays the plan. Run inside a synctest bubble.
func (r *modelRun) execute() {
	r.t.Helper()
	r.checkState()
	for r.i = 0; r.i < len(r.plan); r.i++ {
		e := r.plan[r.i]
		if e.kind == evIdle {
			time.Sleep(e.idle)
			r.checkState()
			continue
		}
		t1, f1 := r.admitOne()
		if t1 == nil {
			continue
		}
		start1 := time.Now()
		gap1 := start1.Sub(r.lastStart)
		r.lastStart = start1

		if e.pair && r.i+1 < len(r.plan) && r.plan[r.i+1].kind == evResponse && !t1.probe {
			e2 := r.plan[r.i+1]
			t2, f2 := r.admitOne()
			if t2 == nil {
				r.resolve(t1, f1, outcomeOf(e, gap1))
				continue
			}
			start2 := time.Now()
			gap2 := start2.Sub(r.lastStart)
			r.lastStart = start2
			r.resolve(t1, f1, outcomeOf(e, gap1))
			r.i++
			r.resolve(t2, f2, outcomeOf(e2, gap2))
			continue
		}
		time.Sleep(e.rtt)
		r.resolve(t1, f1, outcomeOf(e, gap1))
	}
}

// --- the tests ----------------------------------------------------------

// Mixed regimes, every invariant after every event.
func TestAdaptiveModelInvariants(t *testing.T) {
	for _, seed := range modelSeeds(t) {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				run := newModelRun(t, seed, genEvents(seed, modelEvents, regimeMixed, 0), 0)
				run.execute()
			})
		})
	}
}

// A server that refuses whenever two requests start less than X apart.
//
// The controller should settle near X instead of oscillating. Derivation of
// the band: the interval only falls below X by a speed-up step from at or
// above X, so a refusal is recorded at a cut c with Speedup*X <= c < X (the
// executor sends requests exactly one interval apart, so the server's gap is
// the interval). The controller then recovers to (1+Headroom) times the
// median of its cuts and stays there, so the settled interval lies in
//
//	[(1+Headroom)*Speedup*X, (1+Headroom)*X)
//
// and is above X when (1+Headroom)*Speedup >= 1 -- true for the policies
// drawn here (Speedup 0.9, Headroom 0.25 or 0.5).
//
// Getting there takes time. A refusal doubles the interval to 2c, and every
// step back down to the floor is faster than the caller's start (the start
// is at most 4X), so it waits ProbeWindow clean responses: about
// ln(2/(1+Headroom))/ln(1/Speedup) steps -- five at Headroom 0.25 -- of
// ProbeWindow requests each, at intervals up to 2X. When CutMemory expires
// the controller re-probes, which costs two or three ProbeWindows near the
// band, one refusal, and that recovery again. So the median sits in the band
// only if CutMemory is long against the recovery; with ten minutes and a
// ProbeWindow of 100 the cut was forgotten about when the floor was reached,
// and the median sat a step or two above the band -- by design, not a fault.
// The test therefore requires CutMemory >= 4 x recovery and checks that it
// holds for the policy drawn (30 minutes or an hour, against a run of one to
// two hours of virtual time).
//
// The run also must not oscillate: refusals in the second half are at most
// two per CutMemory period, one re-probe each plus slack.
func TestAdaptiveModelHardCeiling(t *testing.T) {
	for _, seed := range modelSeeds(t) {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				r := rand.New(rand.NewPCG(seed, 0xce11))
				x := time.Duration(100+r.IntN(300)) * time.Millisecond
				run := newModelRun(t, seed, genEvents(seed, modelEvents, regimeCeiling, x), x)
				t0 := time.Now()
				run.execute()
				elapsed := time.Since(t0)

				p := run.m.p
				steps := math.Ceil(math.Log(2/(1+p.Headroom)) / math.Log(1/p.Speedup))
				recovery := time.Duration(steps * float64(p.ProbeWindow) * float64(2*x))
				if p.CutMemory < 4*recovery {
					t.Fatalf("precondition: CutMemory %v is under 4 x recovery %v; the band does not apply", p.CutMemory, recovery)
				}
				var half []time.Duration
				for k, idx := range run.sampleIdx {
					if idx >= len(run.plan)/2 {
						half = append(half, run.samples[k])
					}
				}
				slices.Sort(half)
				med := half[len(half)/2]
				lo := time.Duration(float64(x) * (1 + p.Headroom) * p.Speedup)
				hi := time.Duration(float64(x) * (1 + p.Headroom))
				if med < lo || med > hi {
					run.i = len(run.plan)
					run.fail("ceiling %v: median interval over the second half %v, want within [%v, %v]", x, med, lo, hi)
				}

				late := 0
				for _, idx := range run.refusedAt {
					if idx >= len(run.plan)/2 {
						late++
					}
				}
				// Half the run's wall time, in CutMemory periods, rounded up;
				// a re-probe per period may cost up to two refusals.
				periods := int(elapsed/2/p.CutMemory) + 1
				if late > 2*periods {
					run.i = len(run.plan)
					run.fail("ceiling %v: %d refusals in the second half (%v), want at most %d: it oscillates",
						x, late, elapsed/2, 2*periods)
				}
				t.Logf("ceiling %v: median %v in [%v, %v], %d refusals (%d late), %v virtual",
					x, med, lo, hi, len(run.refusedAt), late, elapsed.Round(time.Second))
			})
		})
	}
}

type phase struct {
	regime regime
	n      int
}

// genPhases is a plan of fixed phases, for runs whose shape matters more
// than its variety. Same seed, same plan.
func genPhases(seed uint64, phases ...phase) []modelEvent {
	r := rand.New(rand.NewPCG(seed, 0x9a5e))
	var out []modelEvent
	for _, ph := range phases {
		for range ph.n {
			out = append(out, genOne(r, ph.regime, 0))
		}
	}
	return out
}

// After an outage the client must come back down. Refusals in an outage
// arrive at an interval the previous refusal doubled, at Max, or at a probe;
// none of them says where a rate limit lies, and remembering them as cuts
// held the client at Max for CutMemory once the server was answering again.
// Within a few windows of clean responses the interval must be below Max.
func TestAdaptiveModelRecoversAfterOutage(t *testing.T) {
	const before, outage, after = 2000, 1500, 3000
	for _, seed := range modelSeeds(t) {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				plan := genPhases(seed, phase{regimeClean, before}, phase{regimeOutage, outage}, phase{regimeClean, after})
				run := newModelRun(t, seed, plan, 0)
				run.execute()

				p := run.m.p
				k := 3 * max(p.Window, p.ProbeWindow)
				seen := 0
				for n, idx := range run.sampleIdx {
					if idx < before+outage {
						continue
					}
					if run.samples[n] < p.Max {
						return
					}
					if seen++; seen >= k {
						break
					}
				}
				run.i = before + outage
				run.fail("interval still at Max %v after %d clean responses following the outage (cuts %v)", p.Max, seen, run.m.cuts)
			})
		})
	}
}

// The generator is deterministic per seed and its distributions are what
// the executor claims they are.
func TestAdaptiveModelGenerator(t *testing.T) {
	a := genEvents(42, modelEvents, regimeMixed, 0)
	b := genEvents(42, modelEvents, regimeMixed, 0)
	if !slices.Equal(a, b) {
		t.Fatal("the same seed gave two different plans")
	}
	if slices.Equal(a, genEvents(43, modelEvents, regimeMixed, 0)) {
		t.Fatal("seeds 42 and 43 gave the same plan")
	}
	if len(a) != modelEvents {
		t.Fatalf("plan of %d events, want %d", len(a), modelEvents)
	}

	// A mixed plan exercises every kind of event.
	var regimes [regimeMixed]int
	var refused, withRA, longRA, unavailable, internal, transport, pairs, idleShort, idleLong int
	for _, e := range a {
		regimes[e.regime]++
		switch {
		case e.kind == evIdle:
			if e.idle <= 0 {
				t.Fatalf("idle gap %v", e.idle)
			}
			if e.idle < time.Minute {
				idleShort++
			}
			if e.idle > 30*time.Minute {
				idleLong++
			}
			continue
		case e.status == http.StatusTooManyRequests:
			refused++
		case e.status == http.StatusServiceUnavailable:
			unavailable++
		case e.status == http.StatusInternalServerError:
			internal++
		case e.status == 0:
			transport++
		}
		if e.rtt <= 0 {
			t.Fatalf("latency %v", e.rtt)
		}
		if e.retryAfter > 0 {
			withRA++
		}
		if e.retryAfter > time.Hour {
			longRA++
		}
		if e.pair {
			pairs++
		}
	}
	for g, n := range regimes {
		if g != int(regimeCeiling) && n == 0 {
			t.Errorf("mixed plan has no %s phase", regime(g))
		}
	}
	if regimes[regimeCeiling] != 0 {
		t.Errorf("mixed plan has %d ceiling events", regimes[regimeCeiling])
	}
	for name, n := range map[string]int{
		"429": refused, "503": unavailable, "500": internal, "transport error": transport,
		"Retry-After": withRA, "Retry-After past MaxCooldown": longRA, "429 without Retry-After": refused + unavailable - withRA,
		"pair": pairs, "idle under a minute": idleShort, "idle past 30m": idleLong,
	} {
		if n == 0 {
			t.Errorf("mixed plan has no %s", name)
		}
	}

	// Clean latency: bimodal, cache hits about one in twelve, body near 48ms.
	clean := genEvents(7, 50_000, regimeClean, 0)
	var rtts []time.Duration
	hits := 0
	for _, e := range clean {
		if e.kind != evResponse {
			continue
		}
		if e.status != http.StatusOK {
			t.Fatalf("clean regime produced status %d", e.status)
		}
		rtts = append(rtts, e.rtt)
		if e.rtt < 30*time.Millisecond {
			hits++
		}
	}
	slices.Sort(rtts)
	if med := rtts[len(rtts)/2]; med < 42*time.Millisecond || med > 52*time.Millisecond {
		t.Errorf("clean median latency %v, want about 48ms", med)
	}
	if f := float64(hits) / float64(len(rtts)); f < 0.07 || f > 0.10 {
		t.Errorf("cache-hit share %.3f, want about 1/12", f)
	}
	if p99 := rtts[len(rtts)*99/100]; p99 < 60*time.Millisecond || p99 > 150*time.Millisecond {
		t.Errorf("clean p99 latency %v, want a thin tail", p99)
	}

	// Refusal rates by regime.
	rate := func(g regime) float64 {
		n, bad := 0, 0
		for _, e := range genEvents(9, 50_000, g, 0) {
			if e.kind != evResponse {
				continue
			}
			n++
			if e.status == http.StatusTooManyRequests || e.status == http.StatusServiceUnavailable {
				bad++
			}
		}
		return float64(bad) / float64(n)
	}
	if f := rate(regimeSporadic); f < 0.02 || f > 0.04 {
		t.Errorf("sporadic refusal rate %.3f, want about 0.03", f)
	}
	if f := rate(regimeOutage); f < 0.8 || f > 0.9 {
		t.Errorf("outage refusal rate %.3f, want about 0.85", f)
	}

	// A slow phase is several times the clean median.
	var slow []time.Duration
	for _, e := range genEvents(11, 5000, regimeSlow, 0) {
		if e.kind == evResponse {
			slow = append(slow, e.rtt)
		}
	}
	slices.Sort(slow)
	if med := slow[len(slow)/2]; med < 3*42*time.Millisecond {
		t.Errorf("slow median %v, want at least three times the clean one", med)
	}

	// Ceiling plans: every event carries the ceiling, no idle, no pairs.
	for _, e := range genEvents(5, 2000, regimeCeiling, 150*time.Millisecond) {
		if e.kind != evResponse || e.ceiling != 150*time.Millisecond || e.pair {
			t.Fatalf("ceiling plan has event %s", e)
		}
	}
}
