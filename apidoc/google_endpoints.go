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
// @Param        id  query  string  true   "App package id, e.g. com.whatsapp"
// @Param        hl  query  string  false  "UI language (ISO 639), e.g. en"
// @Param        gl  query  string  false  "Country (ISO 3166), e.g. us"
// @Success      200  {object}  App  "App parsed from AF_initDataCallback ds:5[1][2]"
// @x-rpcid "none"
// @x-data-block "ds:5"
// @x-response-encoding "AF_initDataCallback"
// @x-payload-node "[1][2]"
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
// @Param        id  query  string  true   "App package id"
// @Param        hl  query  string  false  "UI language"
// @Param        gl  query  string  true   "Country to probe; one request per country"
// @Success      200  {object}  AvailabilityResult  "Aggregated availability sweep; per-country Status read from ds:5[1][2][18]"
// @x-rpcid "none"
// @x-data-block "ds:5"
// @x-response-encoding "AF_initDataCallback"
// @x-payload-node "[1][2][18]"
// @Router       /store/apps/details(availability) [get]
func getAppAvailability() {}

// searchApps documents the search-results page.
//
// @Summary      Search apps (HTML)
// @Description  Returns text/html; []SearchResult and an optional pagination
// @Description  token are parsed from the embedded AF_initDataCallback blocks.
// @Id           searchApps
// @Tags         html-endpoints
// @Produce      html
// @Param        q      query  string  true   "Search query"
// @Param        c      query  string  true   "Corpus; the scraper always sends 'apps'"
// @Param        price  query  int     false  "Price filter: 0=all, 1=free, 2=paid"
// @Param        hl     query  string  false  "UI language"
// @Param        gl     query  string  false  "Country"
// @Success      200  {array}  SearchResult  "Search results parsed from AF_initDataCallback"
// @x-rpcid "none"
// @x-response-encoding "AF_initDataCallback"
// @Router       /store/search [get]
func searchApps() {}

// topApps documents the top-charts landing page used as a cluster source.
//
// @Summary      Top charts page (HTML)
// @Description  Returns text/html. Used both to parse the initial []SearchResult
// @Description  and to discover collection/cluster URLs for paging.
// @Id           topApps
// @Tags         html-endpoints
// @Produce      html
// @Param        hl  query  string  false  "UI language"
// @Param        gl  query  string  false  "Country"
// @Success      200  {array}  SearchResult  "Top apps parsed from AF_initDataCallback"
// @x-rpcid "none"
// @x-response-encoding "AF_initDataCallback"
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
// @Param        category  path   string  true   "Category id, e.g. GAME or PRODUCTIVITY"
// @Param        hl        query  string  false  "UI language"
// @Param        gl        query  string  false  "Country"
// @Success      200  {array}  SearchResult  "Category apps parsed from AF_initDataCallback"
// @x-rpcid "none"
// @x-response-encoding "AF_initDataCallback"
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
// @Param        id  query  string  true   "Numeric developer id"
// @Param        hl  query  string  false  "UI language"
// @Param        gl  query  string  false  "Country"
// @Success      200  {array}  SearchResult  "Developer apps parsed from AF_initDataCallback"
// @x-rpcid "none"
// @x-response-encoding "AF_initDataCallback"
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
// @Param        id  query  string  true   "String developer name"
// @Param        hl  query  string  false  "UI language"
// @Param        gl  query  string  false  "Country"
// @Success      200  {array}  SearchResult  "Developer apps parsed from AF_initDataCallback"
// @x-rpcid "none"
// @x-response-encoding "AF_initDataCallback"
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
// @Param        id  query  string  true   "App package id"
// @Param        hl  query  string  false  "UI language"
// @Param        gl  query  string  false  "Country"
// @Success      200  {object}  DataSafety  "Data safety parsed from AF_initDataCallback ds:3"
// @x-rpcid "none"
// @x-data-block "ds:3"
// @x-response-encoding "AF_initDataCallback"
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
// @Param        clusterPath  path   string  true   "Absolute cluster/collection path from a prior page"
// @Param        hl           query  string  false  "UI language"
// @Param        gl           query  string  false  "Country"
// @Success      200  {array}  SearchResult  "Cluster apps parsed from AF_initDataCallback"
// @x-rpcid "none"
// @x-response-encoding "AF_initDataCallback"
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
// @Param        rpcids  query     string  true   "vyAe2"
// @Param        f.req   formData  string  true   "URL-encoded JSON envelope [[['vyAe2','<inner-args>',null,'generic']]]"
// @Success      200     {array}   SearchResult  "Decoded from batchexecute envelope"
// @x-rpcid "vyAe2"
// @x-response-encoding "batchexecute-envelope"
// @Router       /_/PlayStoreUi/data/batchexecute(vyAe2) [post]
func batchListVyAe2() {}

// batchPaginateQnKhOb documents the qnKhOb pagination RPC.
//
// @Summary      Paginate list/cluster/search RPC (qnKhOb)
// @Description  POST /_/PlayStoreUi/data/batchexecute?rpcids=qnKhOb. Advances a
// @Description  list, cluster or search result set using a continuation token.
// @Description  Returns the batchexecute envelope; []SearchResult plus the next
// @Description  token are decoded from the inner payload.
// @Id           batchPaginateQnKhOb
// @Tags         batchexecute-rpc
// @Accept       x-www-form-urlencoded
// @Produce      plain
// @Param        rpcids  query     string  true   "qnKhOb"
// @Param        f.req   formData  string  true   "URL-encoded JSON carrying the continuation token"
// @Success      200     {array}   SearchResult  "Decoded from batchexecute envelope; includes next token"
// @x-rpcid "qnKhOb"
// @x-response-encoding "batchexecute-envelope"
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
// @Param        f.req  formData  string  true   "URL-encoded JSON envelope [[['oCPfdb','<inner-args>',null,'generic']]]"
// @Success      200    {object}  ReviewsResult  "Decoded from batchexecute envelope"
// @x-rpcid "oCPfdb"
// @x-response-encoding "batchexecute-envelope"
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
// @Param        rpcids  query     string  true   "xdSrCf"
// @Param        f.req   formData  string  true   "URL-encoded JSON envelope [[['xdSrCf','<inner-args>',null,'1']]]"
// @Success      200     {array}   Permission  "Decoded from batchexecute envelope"
// @x-rpcid "xdSrCf"
// @x-response-encoding "batchexecute-envelope"
// @Router       /_/PlayStoreUi/data/batchexecute(xdSrCf) [post]
func batchPermissionsXdSrCf() {}

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
// @Param        rpcids  query     string  true   "IJ4APc"
// @Param        f.req   formData  string  true   "URL-encoded JSON envelope [[['IJ4APc','<inner-args>']]]"
// @Success      200     {array}   string  "Suggested terms decoded from batchexecute envelope"
// @x-rpcid "IJ4APc"
// @x-response-encoding "batchexecute-envelope"
// @Router       /_/PlayStoreUi/data/batchexecute(IJ4APc) [post]
func batchSuggestIJ4APc() {}
