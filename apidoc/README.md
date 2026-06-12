# apidoc — OpenAPI spec for Google Play's private endpoints

This is a **documentation-only, nested Go module**. It holds an auto-generated
OpenAPI 2.0 (Swagger) description of the **private, undocumented** Google Play
Store HTTP endpoints that the root `google-play-scraper` library calls.

It lives in its own module so the root library can stay **zero-dependency**: only
this directory depends on [swaggo/swag](https://github.com/swaggo/swag). The
dependency direction is `apidoc → root`; nothing in the root imports `apidoc`.

> **Disclaimer.** These endpoints are not a public, supported Google API. This
> project is not affiliated with or endorsed by Google. The endpoints are
> undocumented and may change without notice; accessing them may be subject to
> the Google Play / Google Terms of Service. The spec is provided for
> interoperability and educational purposes only.

## Layout

| File | Purpose |
|------|---------|
| `doc.go` | General API info (`@title`, `@host play.google.com`, disclaimer) + `//go:generate`. |
| `models.go` | Type aliases re-exporting the root models (`App`, `ReviewsResult`, …) plus `BatchExecuteEnvelope` / `FReqBody` describing the wire format. |
| `google_endpoints.go` | One annotated stub per operation (no runtime code). |
| `docs/` | Generated output: `docs.go`, `swagger.json`, `swagger.yaml` (committed). |
| `drift_test.go` | Tests that the spec stays in sync with the scraper source. |

## Documented endpoints

- **GET** HTML pages returning `AF_initDataCallback` `ds:N` blocks:
  `/store/apps/details` (app + availability probe), `/store/search`,
  `/store/apps/top`, `/store/apps/category/{category}`, `/store/apps/dev`,
  `/store/apps/developer`, `/store/apps/datasafety`, and absolute cluster URLs.
- **POST** `/_/PlayStoreUi/data/batchexecute` RPCs (one operation each):
  `vyAe2` (lists), `qnKhOb` (pagination), `oCPfdb` (reviews), `xdSrCf`
  (permissions), `IJ4APc` (suggestions).

Because OpenAPI keys operations by path, and swaggo rejects `#`/`?` in router
paths, the five `batchexecute` RPCs and the availability probe are disambiguated
with a synthetic trailing `(rpcid)` segment (e.g.
`/_/PlayStoreUi/data/batchexecute(vyAe2)`). That segment is **not** part of the
real request — the true path, rpcid, and response encoding are restated in each
operation's description and in `x-rpcid` / `x-response-encoding` extensions.

## Regenerate

```sh
make gen        # or ./gen.sh, or: go generate ./...
make test       # drift tests: rpcid/path coverage + spec freshness
make verify     # regenerate and fail if docs/ changed (CI guard)
```
