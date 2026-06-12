package lightfeed

import (
	"context"
	"fmt"
	"time"

	"github.com/chromedp/chromedp"
	gps "github.com/kryuchenko/google-play-scraper"
)

// stableRounds is how many consecutive scroll rounds without new apps end
// pagination: the feed is considered exhausted once the count holds steady this
// many times.
const stableRounds = 3

// collectAppLinksJS reads every distinct app-details link the page currently
// renders, returning them as tab-separated `id\thref\ttitle\ticon` lines.
// Running one JS snippet per round (rather than chromedp's node queries) keeps
// us on the CDP subset Lightpanda implements: it only needs Runtime.evaluate.
const collectAppLinksJS = `
(() => {
  const out = [];
  const seen = new Set();
  for (const a of document.querySelectorAll('a[href*="/store/apps/details?id="]')) {
    const m = a.href.match(/[?&]id=([^&]+)/);
    if (!m) continue;
    const id = decodeURIComponent(m[1]);
    if (seen.has(id)) continue;
    seen.add(id);
    const img = a.querySelector('img');
    const title = (a.getAttribute('aria-label') || a.textContent || '').trim();
    out.push([id, a.href, title, img ? (img.src || '') : ''].join('\t'));
  }
  return out.join('\n');
})()
`

// scrollFeed connects to the browser at ws, navigates to req.URL, and scrolls
// until the app set stabilizes, the limit/round cap is hit, or ctx expires. It
// returns the harvested apps as thin SearchResults.
//
// chromedp's RemoteAllocator speaks to Lightpanda's CDP subset; we restrict
// ourselves to Page.navigate and Runtime.evaluate, which Lightpanda supports,
// instead of chromedp's higher-level node actions that assume full CDP.
func scrollFeed(ctx context.Context, ws string, req gps.FeedRequest, cfg config) ([]gps.SearchResult, error) {
	allocCtx, cancelAlloc := chromedp.NewRemoteAllocator(ctx, ws)
	defer cancelAlloc()

	browserCtx, cancelBrowser := chromedp.NewContext(allocCtx)
	defer cancelBrowser()

	if err := chromedp.Run(browserCtx, chromedp.Navigate(req.URL)); err != nil {
		return nil, fmt.Errorf("lightfeed: navigate %s: %w", req.URL, err)
	}
	// Let the initial grid render before the first scroll.
	if err := sleep(browserCtx, cfg.throttle); err != nil {
		return nil, err
	}

	results := newLinkSet()
	stable := 0

	for round := 0; round < cfg.scrollRounds; round++ {
		if err := harvest(browserCtx, results); err != nil {
			return nil, err
		}
		if req.Limit > 0 && results.len() >= req.Limit {
			break
		}

		before := results.len()
		if err := chromedp.Run(browserCtx,
			chromedp.Evaluate(`window.scrollTo(0, document.body.scrollHeight)`, nil),
		); err != nil {
			return nil, fmt.Errorf("lightfeed: scroll: %w", err)
		}
		if err := sleep(browserCtx, cfg.throttle); err != nil {
			return nil, err
		}

		if err := harvest(browserCtx, results); err != nil {
			return nil, err
		}
		if results.len() == before {
			if stable++; stable >= stableRounds {
				break // feed has stopped growing
			}
		} else {
			stable = 0
		}
	}

	return results.results(req.Limit), nil
}

// harvest runs the collector JS once and folds the result into set.
func harvest(ctx context.Context, set *linkSet) error {
	var raw string
	if err := chromedp.Run(ctx, chromedp.Evaluate(collectAppLinksJS, &raw)); err != nil {
		return fmt.Errorf("lightfeed: read app links: %w", err)
	}
	set.addRaw(raw)
	return nil
}

// sleep waits for d, returning early if the context is cancelled. chromedp.Sleep
// would do, but a context-aware wait keeps cancellation crisp.
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
