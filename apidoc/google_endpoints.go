package apidoc

// This file contains one annotated stub per private Google Play operation the
// scraper calls. The function bodies are intentionally empty: they exist only to
// carry swaggo annotations. Nothing here is executed.
//
// NOTE on multiple RPCs sharing one path+method: every batchexecute RPC is sent
// as POST /_/PlayStoreUi/data/batchexecute, but OpenAPI (and swaggo) key an
// operation by its path string, so five RPCs on one path would overwrite each
// other. swaggo rejects `#` and `?` in @Router paths, so we disambiguate with a
// synthetic trailing `(rpcid)` segment, e.g.
// /_/PlayStoreUi/data/batchexecute(vyAe2). This segment is NOT part of the real
// request — the true wire path is /_/PlayStoreUi/data/batchexecute and the rpcid
// travels in the rpcids query param and/or the f.req body. The real path, the
// rpcid, and the response encoding are restated in each Description and in the
// x-rpcid / x-response-encoding extensions. The same `(availability)` trick
// separates the availability probe from the app-detail fetch, which share
// GET /store/apps/details but read different data nodes.
//
// NOTE on x-* extensions: swaggo emits vendor extensions from x-name
// annotations whose value must be valid JSON, so the values below are quoted
// JSON strings. We record rpcid, data-block, response-encoding and payload-node
// this way. The same facts are repeated in each Description so the information
// survives even if a tool drops unknown extensions.

// getAppDetails documents the primary app-detail fetch.
//
// @Summary      App details (HTML, ds:5)
// @Description  Returns text/html. The App model is parsed from the
// @Description  AF_initDataCallback `ds:5` block, payload node [1][2].
// @Id           getAppDetails
// @Tags         html-endpoints
// @Produce      html
// @Param        id  query  string  true   "App package id, reverse-domain form, pattern ^[a-zA-Z][a-zA-Z0-9_]*(\.[a-zA-Z][a-zA-Z0-9_]*)+$ (pattern is descriptive only; swag v2 does not emit @Param pattern), e.g. com.whatsapp"  minlength(3)  maxlength(255)  example(com.spotify.music)
// @Param        hl  query  string  false  "UI language, ISO 639-1, usually 2 lowercase letters but locale variants like pt-BR occur, e.g. en"  minlength(2)  maxlength(5)  example(en)
// @Param        gl  query  string  false  "Country, ISO 3166-1 alpha-2 lowercase, pattern ^[a-z]{2}$ (descriptive only), e.g. us"  minlength(2)  maxlength(2)  example(us)
// @Success      200  {object}  App  "App parsed from AF_initDataCallback ds:5[1][2]"
// @Failure      404  {object}  ErrorResponse  "App/listing not found — removed, never existed, or not distributed in this country (gl). Surfaced as StatusError{Code:404}."
// @Failure      429  {object}  ErrorResponse  "Rate-limited / anti-bot challenge. Google may return 429, or 200 redirecting to google.com/sorry (CAPTCHA). Sustained scraping from one IP triggers this."
// @Failure      500  {object}  ErrorResponse  "Upstream Google error (any 5xx). Surfaced as StatusError with the observed code."
// @Router       /store/apps/details [get]
func getAppDetails() {}

// getAppAvailability documents the per-country availability probe. It hits the
// SAME path as getAppDetails but reads a different node, so it is documented as
// a distinct operation via a synthetic fragment.
//
// @Summary      App availability probe (HTML, ds:5 node [18])
// @Description  Reuses GET /store/apps/details per gl country. Availability is
// @Description  derived from node [18] of `ds:5`[1][2]: [18][0]==2 available,
// @Description  ==1 pre-registration, empty/missing not-in-region, HTTP 404
// @Description  not-found. The aggregated sweep yields AvailabilityResult.
// @Id           getAppAvailability
// @Tags         html-endpoints
// @Produce      html
// @Param        id  query  string  true   "App package id, reverse-domain form (pattern ^[a-zA-Z][a-zA-Z0-9_]*(\.[a-zA-Z][a-zA-Z0-9_]*)+$, descriptive only), e.g. com.whatsapp"  minlength(3)  maxlength(255)  example(com.spotify.music)
// @Param        hl  query  string  false  "UI language, ISO 639-1, e.g. en"  minlength(2)  maxlength(5)  example(en)
// @Param        gl  query  string  true   "Country to probe, ISO 3166-1 alpha-2 lowercase (pattern ^[a-z]{2}$, descriptive only); one request per country, e.g. us"  minlength(2)  maxlength(2)  example(us)
// @Success      200  {object}  AvailabilityResult  "Aggregated availability sweep; per-country Status read from ds:5[1][2][18]"
// @Failure      404  {object}  ErrorResponse  "Probed country has no listing; at the per-country probe a 404 maps to Status not_found (StatusError{Code:404}) rather than failing the whole sweep."
// @Failure      429  {object}  ErrorResponse  "Rate-limited / anti-bot challenge. Google may return 429, or 200 redirecting to google.com/sorry (CAPTCHA). Sweeping many countries from one IP makes this likely; the failing country maps to Status error."
// @Failure      500  {object}  ErrorResponse  "Upstream Google error (any 5xx); the failing country maps to Status error."
// @Router       /store/apps/details(availability) [get]
func getAppAvailability() {}

// searchApps documents the search-results page.
//
// @Summary      Search apps (HTML)
// @Description  Returns text/html; []SearchResult and an optional pagination
// @Description  token are parsed from the embedded AF_initDataCallback blocks.
// @Description  Empirical limit: this initial page yields only ~20-30 results.
// @Description  Search.Num is validated up to searchMaxNum=250, but Google
// @Description  currently rejects the qnKhOb continuation token, so deeper
// @Description  pages are unreachable and the real return is one page.
// @Id           searchApps
// @Tags         html-endpoints
// @Produce      html
// @Param        q      query  string  true   "Search query term"  minlength(1)  maxlength(255)  example(minecraft)
// @Param        c      query  string  true   "Corpus; the scraper always sends 'apps'"  enums(apps)  example(apps)
// @Param        price  query  int     false  "Price filter: 0=all, 1=free, 2=paid (see search.go getPriceValue)"  enums(0, 1, 2)  minimum(0)  maximum(2)  example(0)
// @Param        hl     query  string  false  "UI language, ISO 639-1, e.g. en"  minlength(2)  maxlength(5)  example(en)
// @Param        gl     query  string  false  "Country, ISO 3166-1 alpha-2 lowercase (pattern ^[a-z]{2}$, descriptive only), e.g. us"  minlength(2)  maxlength(2)  example(us)
// @Success      200  {array}  SearchResult  "Search results parsed from AF_initDataCallback. No match is NOT a 404: Google returns 200 with an empty result set, decoded as an empty array."
// @Failure      429  {object}  ErrorResponse  "Rate-limited / anti-bot challenge. Google may return 429, or 200 redirecting to google.com/sorry (CAPTCHA). Sustained scraping from one IP triggers this."
// @Failure      500  {object}  ErrorResponse  "Upstream Google error (any 5xx). Surfaced as StatusError with the observed code."
// @Router       /store/search [get]
func searchApps() {}

// topApps documents the top-charts landing page used as a cluster source.
//
// @Summary      Top charts page (HTML)
// @Description  Returns text/html. Used both to parse the initial []SearchResult
// @Description  and to discover collection/cluster URLs for paging.
// @Description  Note: Google answers this path with a 302 to /store/apps before
// @Description  the terminal 200, so the client must follow redirects.
// @Id           topApps
// @Tags         html-endpoints
// @Produce      html
// @Param        hl  query  string  false  "UI language, ISO 639-1, e.g. en"  minlength(2)  maxlength(5)  example(en)
// @Param        gl  query  string  false  "Country, ISO 3166-1 alpha-2 lowercase (pattern ^[a-z]{2}$, descriptive only), e.g. us"  minlength(2)  maxlength(2)  example(us)
// @Success      200  {array}  SearchResult  "Top apps parsed from AF_initDataCallback"
// @Failure      429  {object}  ErrorResponse  "Rate-limited / anti-bot challenge. Google may return 429, or 200 redirecting to google.com/sorry (CAPTCHA). Sustained scraping from one IP triggers this."
// @Failure      500  {object}  ErrorResponse  "Upstream Google error (any 5xx). Surfaced as StatusError with the observed code."
// @Router       /store/apps/top [get]
func topApps() {}

// categoryApps documents the per-category landing page.
//
// @Summary      Category page (HTML)
// @Description  Returns text/html for a single category; parsed like the top
// @Description  charts page into []SearchResult plus cluster URLs.
// @Id           categoryApps
// @Tags         html-endpoints
// @Produce      html
// @Param        category  path   string  true   "Play category id, e.g. GAME, GAME_ACTION or PRODUCTIVITY. One of the 54 ids in AllCategories (constants.go); not enumerated here to avoid drift as Google adds/removes categories."  example(GAME_ACTION)
// @Param        hl        query  string  false  "UI language, ISO 639-1, e.g. en"  minlength(2)  maxlength(5)  example(en)
// @Param        gl        query  string  false  "Country, ISO 3166-1 alpha-2 lowercase (pattern ^[a-z]{2}$, descriptive only), e.g. us"  minlength(2)  maxlength(2)  example(us)
// @Success      200  {array}  SearchResult  "Category apps parsed from AF_initDataCallback"
// @Failure      404  {object}  ErrorResponse  "No such category page — the {category} id is not a recognized Play category. Surfaced as StatusError{Code:404}."
// @Failure      429  {object}  ErrorResponse  "Rate-limited / anti-bot challenge. Google may return 429, or 200 redirecting to google.com/sorry (CAPTCHA). Sustained scraping from one IP triggers this."
// @Failure      500  {object}  ErrorResponse  "Upstream Google error (any 5xx). Surfaced as StatusError with the observed code."
// @Router       /store/apps/category/{category} [get]
func categoryApps() {}

// developerAppsNumeric documents the numeric-id developer page.
//
// @Summary      Developer apps by numeric id (HTML)
// @Description  Returns text/html. Used when the developer id is the numeric
// @Description  internal id; results parsed into []SearchResult.
// @Id           developerAppsNumeric
// @Tags         html-endpoints
// @Produce      html
// @Param        id  query  string  true   "Numeric developer id, digits only (pattern ^[0-9]+$, descriptive only), e.g. 5700313618786177705"  minlength(1)  maxlength(32)  example(5700313618786177705)
// @Param        hl  query  string  false  "UI language, ISO 639-1, e.g. en"  minlength(2)  maxlength(5)  example(en)
// @Param        gl  query  string  false  "Country, ISO 3166-1 alpha-2 lowercase (pattern ^[a-z]{2}$, descriptive only), e.g. us"  minlength(2)  maxlength(2)  example(us)
// @Success      200  {array}  SearchResult  "Developer apps parsed from AF_initDataCallback"
// @Failure      404  {object}  ErrorResponse  "No such developer page — the numeric id does not exist or is not distributed in this country (gl). Surfaced as StatusError{Code:404}."
// @Failure      429  {object}  ErrorResponse  "Rate-limited / anti-bot challenge. Google may return 429, or 200 redirecting to google.com/sorry (CAPTCHA). Sustained scraping from one IP triggers this."
// @Failure      500  {object}  ErrorResponse  "Upstream Google error (any 5xx). Surfaced as StatusError with the observed code."
// @Router       /store/apps/dev [get]
func developerAppsNumeric() {}

// developerAppsName documents the string-id developer page.
//
// @Summary      Developer apps by name (HTML)
// @Description  Returns text/html. Used when the developer id is a human-readable
// @Description  name; results parsed into []SearchResult.
// @Id           developerAppsName
// @Tags         html-endpoints
// @Produce      html
// @Param        id  query  string  true   "Human-readable developer name, e.g. Google LLC"  minlength(1)  maxlength(255)  example(Google LLC)
// @Param        hl  query  string  false  "UI language, ISO 639-1, e.g. en"  minlength(2)  maxlength(5)  example(en)
// @Param        gl  query  string  false  "Country, ISO 3166-1 alpha-2 lowercase (pattern ^[a-z]{2}$, descriptive only), e.g. us"  minlength(2)  maxlength(2)  example(us)
// @Success      200  {array}  SearchResult  "Developer apps parsed from AF_initDataCallback"
// @Failure      404  {object}  ErrorResponse  "No such developer page — the name does not match a developer or is not distributed in this country (gl). Surfaced as StatusError{Code:404}."
// @Failure      429  {object}  ErrorResponse  "Rate-limited / anti-bot challenge. Google may return 429, or 200 redirecting to google.com/sorry (CAPTCHA). Sustained scraping from one IP triggers this."
// @Failure      500  {object}  ErrorResponse  "Upstream Google error (any 5xx). Surfaced as StatusError with the observed code."
// @Router       /store/apps/developer [get]
func developerAppsName() {}

// getDataSafety documents the data-safety page.
//
// @Summary      Data safety (HTML, ds:3)
// @Description  Returns text/html. The DataSafety model is parsed from the
// @Description  AF_initDataCallback `ds:3` block.
// @Id           getDataSafety
// @Tags         html-endpoints
// @Produce      html
// @Param        id  query  string  true   "App package id, reverse-domain form (pattern ^[a-zA-Z][a-zA-Z0-9_]*(\.[a-zA-Z][a-zA-Z0-9_]*)+$, descriptive only), e.g. com.whatsapp"  minlength(3)  maxlength(255)  example(com.spotify.music)
// @Param        hl  query  string  false  "UI language, ISO 639-1, e.g. en"  minlength(2)  maxlength(5)  example(en)
// @Param        gl  query  string  false  "Country, ISO 3166-1 alpha-2 lowercase (pattern ^[a-z]{2}$, descriptive only), e.g. us"  minlength(2)  maxlength(2)  example(us)
// @Success      200  {object}  DataSafety  "Data safety parsed from AF_initDataCallback ds:3. Unlike /store/apps/details, this page does NOT 404 for a removed or unknown app: it returns 200 with an empty ds:3 block and DataSafety() yields a zero-value result (nil error)."
// @Failure      400  {object}  ErrorResponse  "Missing the required id query parameter — Google returns 400 for this endpoint (not 404)."
// @Failure      429  {object}  ErrorResponse  "Rate-limited / anti-bot challenge. Google may return 429, or 200 redirecting to google.com/sorry (CAPTCHA). Sustained scraping from one IP triggers this."
// @Failure      500  {object}  ErrorResponse  "Upstream Google error (any 5xx). Surfaced as StatusError with the observed code."
// @Router       /store/apps/datasafety [get]
func getDataSafety() {}

// getCluster documents fetching an absolute cluster/collection URL.
//
// @Summary      Cluster page (HTML, absolute URL)
// @Description  GET of an absolute collection/cluster URL discovered on a list
// @Description  or similar-apps page. Returns text/html parsed into
// @Description  []SearchResult. The path is dynamic, hence the {clusterPath}
// @Description  placeholder.
// @Id           getCluster
// @Tags         html-endpoints
// @Produce      html
// @Param        clusterPath  path   string  true   "Absolute cluster/collection path from a prior page"  minlength(1)
// @Param        hl           query  string  false  "UI language, ISO 639-1, e.g. en"  minlength(2)  maxlength(5)  example(en)
// @Param        gl           query  string  false  "Country, ISO 3166-1 alpha-2 lowercase (pattern ^[a-z]{2}$, descriptive only), e.g. us"  minlength(2)  maxlength(2)  example(us)
// @Success      200  {array}  SearchResult  "Cluster apps parsed from AF_initDataCallback"
// @Failure      404  {object}  ErrorResponse  "Cluster/collection URL no longer valid — tokens expire and clusters rotate. Surfaced as StatusError{Code:404}."
// @Failure      429  {object}  ErrorResponse  "Rate-limited / anti-bot challenge. Google may return 429, or 200 redirecting to google.com/sorry (CAPTCHA). Sustained scraping from one IP triggers this."
// @Failure      500  {object}  ErrorResponse  "Upstream Google error (any 5xx). Surfaced as StatusError with the observed code."
// @Router       /store/apps/collection/{clusterPath} [get]
func getCluster() {}

// batchListVyAe2 documents the vyAe2 top-list RPC.
//
// @Summary      List apps RPC (vyAe2)
// @Description  POST /_/PlayStoreUi/data/batchexecute?rpcids=vyAe2. Body is the
// @Description  url-encoded f.req from list_payload.txt with __NUM__,
// @Description  __COLLECTION__ and __CATEGORY__ substituted. Returns the
// @Description  batchexecute envelope; []SearchResult is decoded from the inner
// @Description  payload. The #vyAe2 path fragment only disambiguates this
// @Description  operation and is not sent to the server.
// @Id           batchListVyAe2
// @Tags         batchexecute-rpc
// @Accept       x-www-form-urlencoded
// @Produce      plain
// @Param        rpcids  query     string  true   "Fixed rpcid for this operation"  enums(vyAe2)
// @Param        f.req   formData  string  true   "URL-encoded JSON envelope [[['vyAe2','<inner-args>',null,'generic']]]; inner __NUM__ is clamped to listMaxNum=660 (list.go), but see the response note — Google caps the actual result at ~200."
// @Success      200     {array}   SearchResult  "Decoded from batchexecute envelope. Empirical limit: Google returns at most ~200 apps per collection regardless of __NUM__ (660 is only the request-side clamp). An empty/null inner payload means no data (end of the collection), not an error."
// @Failure      429     {object}  ErrorResponse  "Rate-limited / anti-bot challenge. Google may return 429, or 200 redirecting to google.com/sorry (CAPTCHA). Sustained scraping from one IP triggers this."
// @Failure      500     {object}  ErrorResponse  "Upstream Google error (any 5xx). Surfaced as StatusError with the observed code."
// @Router       /_/PlayStoreUi/data/batchexecute(vyAe2) [post]
func batchListVyAe2() {}

// batchPaginateQnKhOb documents the qnKhOb pagination RPC.
//
// @Summary      Paginate list/cluster/search RPC (qnKhOb)
// @Description  POST /_/PlayStoreUi/data/batchexecute?rpcids=qnKhOb. Fetches a
// @Description  recommendation topic by continuation token. Returns the
// @Description  batchexecute envelope; []SearchResult plus an echo token are
// @Description  decoded from the inner payload.
// @Description  Empirical note (reverse-engineered live 2026-06-12): PAGE 1 of a
// @Description  topic IS reachable statelessly. Each "recommended for you" section
// @Description  on the category/cluster HTML page carries the topic's recs_topic
// @Description  query inside its cluster URL gsr param (a field-9 base64url
// @Description  protobuf); re-wrapped into the field-12 form this RPC expects, it
// @Description  returns the topic's apps in one request (see extractFeedTokens).
// @Description  The scraper follows every topic on the page this way. PAGE 2+ of a
// @Description  single topic is SERVER-STATEFUL: the response's own token
// @Description  ([0][3][0]) re-references the same topic and is answered with a
// @Description  200 + NULL payload on replay, and the next topic is allocated
// @Description  server-side per session and never echoed — so a single topic
// @Description  cannot be chained deeper from bare HTTP. The payload template is
// @Description  kept current so this keeps working as Google rebuilds its flags.
// @Description  Review pagination uses a different RPC (oCPfdb).
// @Id           batchPaginateQnKhOb
// @Tags         batchexecute-rpc
// @Accept       x-www-form-urlencoded
// @Produce      plain
// @Param        rpcids  query     string  true   "Fixed rpcid for this operation"  enums(qnKhOb)
// @Param        f.req   formData  string  true   "URL-encoded JSON carrying the continuation token"
// @Success      200     {array}   SearchResult  "Decoded from batchexecute envelope; includes next token. When pagination is exhausted Google returns 200 with a NULL inner payload (handled by decodeBatchEnvelope) — that signals end-of-data, not an error, and the next token is empty."
// @Failure      429     {object}  ErrorResponse  "Rate-limited / anti-bot challenge. Google may return 429, or 200 redirecting to google.com/sorry (CAPTCHA). Sustained scraping from one IP triggers this."
// @Failure      500     {object}  ErrorResponse  "Upstream Google error (any 5xx). Surfaced as StatusError with the observed code."
// @Router       /_/PlayStoreUi/data/batchexecute(qnKhOb) [post]
func batchPaginateQnKhOb() {}

// batchReviewsOCPfdb documents the oCPfdb reviews RPC.
//
// @Summary      Reviews RPC (oCPfdb)
// @Description  POST /_/PlayStoreUi/data/batchexecute. Unlike the other RPCs the
// @Description  scraper does NOT put rpcids in the query string; the rpcid lives
// @Description  only inside the f.req body. Handles both the initial fetch and
// @Description  pagination. Returns the batchexecute envelope; ReviewsResult
// @Description  (reviews + nextToken) is decoded from the inner payload.
// @Id           batchReviewsOCPfdb
// @Tags         batchexecute-rpc
// @Accept       x-www-form-urlencoded
// @Produce      plain
// @Param        sort   query     int       false  "Review sort order (Sort enum, constants.go): 1=helpfulness, 2=newest, 3=rating. Default 2 (newest). NOTE: not a real query field — this value is embedded inside the f.req inner args; it is documented as a parameter only to surface the enum."  enums(1, 2, 3)  minimum(1)  maximum(3)  example(2)
// @Param        count  query     int       false  "Reviews per request; embedded in f.req, capped at 150 by Google (reviews.go). Documented as a parameter only to surface the limit."  minimum(1)  maximum(150)  example(100)
// @Param        f.req  formData  string    true   "URL-encoded JSON envelope [[['oCPfdb','<inner-args>',null,'generic']]]. The sort order, page size and pagination token all live inside the inner args, NOT as query fields."
// @Success      200    {object}  ReviewsResult  "Decoded from batchexecute envelope. An empty/null inner payload means no more reviews (end of pagination), not an error; nextToken is then empty."
// @Failure      429    {object}  ErrorResponse  "Rate-limited / anti-bot challenge. Google may return 429, or 200 redirecting to google.com/sorry (CAPTCHA). Sustained scraping from one IP triggers this."
// @Failure      500    {object}  ErrorResponse  "Upstream Google error (any 5xx). Surfaced as StatusError with the observed code."
// @Router       /_/PlayStoreUi/data/batchexecute(oCPfdb) [post]
func batchReviewsOCPfdb() {}

// batchPermissionsXdSrCf documents the xdSrCf permissions RPC.
//
// @Summary      Permissions RPC (xdSrCf)
// @Description  POST /_/PlayStoreUi/data/batchexecute?rpcids=xdSrCf. Returns the
// @Description  batchexecute envelope; []Permission is decoded from the inner
// @Description  payload.
// @Id           batchPermissionsXdSrCf
// @Tags         batchexecute-rpc
// @Accept       x-www-form-urlencoded
// @Produce      plain
// @Param        rpcids  query     string  true   "Fixed rpcid for this operation"  enums(xdSrCf)
// @Param        f.req   formData  string  true   "URL-encoded JSON envelope [[['xdSrCf','<inner-args>',null,'1']]]"
// @Success      200     {array}   Permission  "Decoded from batchexecute envelope. An empty/null inner payload means the app declares no permissions, not an error."
// @Failure      429     {object}  ErrorResponse  "Rate-limited / anti-bot challenge. Google may return 429, or 200 redirecting to google.com/sorry (CAPTCHA). Sustained scraping from one IP triggers this."
// @Failure      500     {object}  ErrorResponse  "Upstream Google error (any 5xx). Surfaced as StatusError with the observed code."
// @Router       /_/PlayStoreUi/data/batchexecute(xdSrCf) [post]
func batchPermissionsXdSrCf() {}

// batchAppDetailsWs7gDc documents the Ws7gDc app-details RPC.
//
// This is the RPC the app details page is built from: the page's own
// AF_dataServiceRequests map names it as the source of the ds:5 script block,
// along with the exact request body. AppsMany calls it directly rather than
// fetching and scraping a megabyte of rendered HTML, and the payload it returns
// is structurally identical to ds:5.
//
// @Summary      App details RPC (Ws7gDc)
// @Description  POST /_/PlayStoreUi/data/batchexecute?rpcids=Ws7gDc. Returns the
// @Description  batchexecute envelope; the inner payload is the same structure
// @Description  the details page carries in its ds:5 block, and App is decoded
// @Description  from it by the same extractor.
// @Id           batchAppDetailsWs7gDc
// @Tags         batchexecute-rpc
// @Accept       x-www-form-urlencoded
// @Produce      plain
// @Param        rpcids  query     string  true   "Fixed rpcid for this operation"  enums(Ws7gDc)
// @Param        f.req   formData  string  true   "URL-encoded JSON envelope [[['Ws7gDc','<inner-args>',null,'0']]]. The package id sits at [5][0][0] of the inner args; the leading field-number array selects which fields Google returns and is copied verbatim from what the page sends."
// @Success      200     {object}  App  "Decoded from batchexecute envelope. A null inner payload means the app id does not exist, which the scraper reports as an error for that app rather than an empty App."
// @Failure      429     {object}  ErrorResponse  "Rate-limited / anti-bot challenge. Google may return 429, or 200 redirecting to google.com/sorry (CAPTCHA). Sustained scraping from one IP triggers this."
// @Failure      500     {object}  ErrorResponse  "Upstream Google error (any 5xx). Surfaced as StatusError with the observed code."
// @Router       /_/PlayStoreUi/data/batchexecute(Ws7gDc) [post]
func batchAppDetailsWs7gDc() {}

// batchSuggestIJ4APc documents the IJ4APc search-suggest RPC.
//
// @Summary      Search suggestions RPC (IJ4APc)
// @Description  POST /_/PlayStoreUi/data/batchexecute?rpcids=IJ4APc. Returns the
// @Description  batchexecute envelope; a []string of suggested query terms is
// @Description  decoded from the inner payload.
// @Id           batchSuggestIJ4APc
// @Tags         batchexecute-rpc
// @Accept       x-www-form-urlencoded
// @Produce      plain
// @Param        rpcids  query     string  true   "Fixed rpcid for this operation"  enums(IJ4APc)
// @Param        f.req   formData  string  true   "URL-encoded JSON envelope [[['IJ4APc','<inner-args>']]]"
// @Success      200     {array}   string  "Suggested terms decoded from batchexecute envelope. An empty/null inner payload means no suggestions, not an error."
// @Failure      429     {object}  ErrorResponse  "Rate-limited / anti-bot challenge. Google may return 429, or 200 redirecting to google.com/sorry (CAPTCHA). Sustained scraping from one IP triggers this."
// @Failure      500     {object}  ErrorResponse  "Upstream Google error (any 5xx). Surfaced as StatusError with the observed code."
// @Router       /_/PlayStoreUi/data/batchexecute(IJ4APc) [post]
func batchSuggestIJ4APc() {}

// getRobotsTxt documents sitemap discovery: the catalog crawler reads the
// `Sitemap:` directives from robots.txt rather than hardcoding the index URLs.
//
// @Summary      Robots / sitemap discovery (text)
// @Description  GET /robots.txt. Returns text/plain. The catalog enumerator
// @Description  (SitemapIndexURLs) parses the `Sitemap:` directives, currently
// @Description  two indexes under /sitemaps/, which together list all ~83k
// @Description  shards. Read live so it tracks Google's own advertisement.
// @Id           getRobotsTxt
// @Tags         sitemap-endpoints
// @Produce      plain
// @Success      200  {array}   string  "Sitemap index URLs extracted from the Sitemap: directives"
// @Failure      429  {object}  ErrorResponse  "Rate-limited / anti-bot challenge."
// @Failure      500  {object}  ErrorResponse  "Upstream Google error (any 5xx). Surfaced as StatusError with the observed code."
// @Router       /robots.txt [get]
func getRobotsTxt() {}

// getSitemapIndex documents one sitemap-index file (a <sitemapindex> of shards).
//
// @Summary      Sitemap index (XML)
// @Description  GET /sitemaps/sitemaps-index-{n}.xml. Returns application/xml: a
// @Description  <sitemapindex> whose <sitemap><loc> entries point at the
// @Description  per-shard .xml.gz files (…-NNNNN-of-NNNNN.xml.gz). index-0 lists
// @Description  shards 00000..49999, index-1 lists the rest. SitemapShards
// @Description  parses the loc list.
// @Id           getSitemapIndex
// @Tags         sitemap-endpoints
// @Produce      xml
// @Param        n  path  int  true  "Index number advertised in robots.txt (0 or 1)"  enums(0, 1)  example(0)
// @Success      200  {array}   string  "Shard URLs parsed from <sitemapindex>/<sitemap>/<loc>"
// @Failure      404  {object}  ErrorResponse  "No such index number."
// @Failure      429  {object}  ErrorResponse  "Rate-limited / anti-bot challenge."
// @Failure      500  {object}  ErrorResponse  "Upstream Google error (any 5xx). Surfaced as StatusError with the observed code."
// @Router       /sitemaps/sitemaps-index-{n}.xml [get]
func getSitemapIndex() {}

// getSitemapShard documents one gzipped sitemap shard (a <urlset> of store URLs).
//
// @Summary      Sitemap shard (gzipped XML)
// @Description  GET /sitemaps/{shard}.xml.gz. Returns a gzip-compressed <urlset>
// @Description  of whole-store URLs (books, movies, music AND apps interleaved).
// @Description  SitemapShardPackages gunzips it and keeps only the
// @Description  /store/apps/details?id=PKG locs — ~30–55 of ~400 URLs per shard,
// @Description  so a full sweep yields on the order of 3 million app ids.
// @Id           getSitemapShard
// @Tags         sitemap-endpoints
// @Produce      application/gzip
// @Param        shard  path  string  true  "Shard file name, e.g. play_sitemaps_2026-06-08_1780977767-00000-of-83445"  example(play_sitemaps_2026-06-08_1780977767-00000-of-83445)
// @Success      200  {array}   string  "App package ids extracted from the shard's /store/apps/details locs"
// @Failure      404  {object}  ErrorResponse  "No such shard."
// @Failure      429  {object}  ErrorResponse  "Rate-limited / anti-bot challenge."
// @Failure      500  {object}  ErrorResponse  "Upstream Google error (any 5xx). Surfaced as StatusError with the observed code."
// @Router       /sitemaps/{shard}.xml.gz [get]
func getSitemapShard() {}
