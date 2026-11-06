# gpscrape-mcp

An MCP server that gives AI agents read-only access to public Google Play
Store data: app details, search, charts, reviews, similar apps, developer
listings, permissions, data safety, search suggestions and per-country
availability.

It works with any MCP client. Claude Code, Codex CLI and OpenCode start it as a
local process over stdio. ChatGPT, the OpenAI API and other remote clients
reach it over Streamable HTTP.

This is a separate Go module, so the root library keeps its zero-dependency
guarantee. The long sweeps (the sitemap catalog, every review of an app) are
left out on purpose and stay in the [`gpscrape`](../README.md) command. The
server tells the agent so, in its instructions and again in the `play_get_reviews`
tool description (some clients, such as claude.ai and the OpenAI Agents SDK,
do not pass instructions to the model). An agent that can run shell commands
is pointed at `gpscrape` for bulk jobs if it is already installed. If it is
not, the agent pages with the tools or tells the user the job needs the CLI,
and it is told to ask the user before installing it. The instructions open
with the untrusted-data warning and stay short; every tool description stays
under 1000 bytes, where Codex cuts them for servers installed as plugins.

## Install

```bash
go install github.com/kryuchenko/google-play-scraper/mcp/cmd/gpscrape-mcp@latest
```

Requires Go 1.27. Without Go:

- **Prebuilt binaries** for macOS, Linux and Windows are attached to each
  [`mcp-v*` release](https://github.com/kryuchenko/google-play-scraper/releases)
  (the Go module tag is `mcp/v*`; the release uses a tag without the slash,
  which the MCP Registry requires in download URLs).
- **MCPB bundle**: `gpscrape-mcp.mcpb` on the same release installs in one
  step in clients that take MCP bundles, such as Claude Desktop. It carries
  macOS (universal), Linux (amd64 and arm64) and Windows (amd64) binaries.
- **Container**: `docker run -i --rm ghcr.io/kryuchenko/gpscrape-mcp` speaks
  stdio; for HTTP, `docker run --rm -p 8080:8080 -e GPSCRAPE_MCP_TOKEN=<token>
  ghcr.io/kryuchenko/gpscrape-mcp -http 0.0.0.0:8080`.
- **MCP Registry**: listed as `io.github.kryuchenko/gpscrape`, so clients that
  browse the registry can find it.

Every release file and the image carry signed build provenance. Check a
download before running it:

```bash
gh attestation verify gpscrape-mcp.mcpb --repo kryuchenko/google-play-scraper
gh attestation verify oci://ghcr.io/kryuchenko/gpscrape-mcp:<version> --repo kryuchenko/google-play-scraper
```

For agents that also have a shell, the
[`gpscrape` skill](../skills/gpscrape/SKILL.md) explains when to reach for the
CLI instead of these tools.

## Connect a client

**Claude Code**

```bash
claude mcp add gpscrape -- gpscrape-mcp
```

**Codex CLI**: `~/.codex/config.toml`

```toml
[mcp_servers.gpscrape]
command = "gpscrape-mcp"
```

**OpenCode**: `~/.config/opencode/opencode.json` or `opencode.json` in a project

```json
{
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "gpscrape": { "type": "local", "command": ["gpscrape-mcp"], "enabled": true }
  }
}
```

**Over HTTP**: for clients that cannot start a local process

```bash
gpscrape-mcp -http 127.0.0.1:8080                       # this machine only, no token
GPSCRAPE_MCP_TOKEN=$(openssl rand -hex 32) \
  gpscrape-mcp -http :8080                              # any interface, token required
```

The endpoint is `http://<host>:8080/mcp`. Clients send
`Authorization: Bearer <token>`. `GET /healthz` answers `ok` for probes. The
server will not bind a non-loopback address without a token. Put TLS in front
of it (nginx, Caddy) before exposing it to the internet. Without a token the
server refuses requests whose `Host` is not local (DNS-rebinding protection),
so a reverse proxy that forwards the public `Host` needs a token set.
Every method rejects an invalid `Origin` with 403, even if Fetch Metadata
claims the request is same-origin. Native clients can omit `Origin`; browser
origins must be HTTP(S) and match the request's public `Host`.

HTTP is sessionless: no `Mcp-Session-Id` is issued, and GET/DELETE return 405.
Protocol 2026-07-28 requests stop upstream work when their HTTP request is
cancelled. Legacy HTTP clients' cancellation notifications cannot reach work
running in a different request's temporary session; the per-call timeout
still bounds that work. Stdio cancellation uses the persistent connection.

Give the token in `$GPSCRAPE_MCP_TOKEN` or in a file with `-token-file`
(a Docker or systemd secret, for instance). There is no `-token` flag: a
secret on the command line is visible to every user of the machine in `ps`.

**ChatGPT** cannot start a local process and cannot send a token: its custom
apps take OAuth or nothing. Host the server yourself (a VPS, or your own
machine behind a tunnel such as Cloudflare Tunnel) with HTTPS in front, start
it with `-secret-path`, and give ChatGPT the token as the last part of the
URL:

```
https://play-mcp.example.com/mcp/<GPSCRAPE_MCP_TOKEN>
```

Add it in ChatGPT as a custom app (developer mode) with
**No authentication**: the secret path is the password. It is off unless
`-secret-path` is given, because a token in a URL ends up in proxy logs and
browser history; any path under `/mcp/` answers 404 without it, and so does a
wrong token with it. Use a long random token (`openssl rand -hex 32`); the
server warns when it is shorter than 24 characters. Keep your proxy from
logging request paths, and rotate the token if the URL leaks. Requests to
Google Play then come from the host's IP, and Google rate-limits data-centre
addresses sooner than a home one.

Remote clients that can set a header point at the URL:

```toml
# Codex
[mcp_servers.gpscrape]
url = "https://play-mcp.example.com/mcp"
bearer_token_env_var = "GPSCRAPE_MCP_TOKEN"
```

```json
// OpenCode
"gpscrape": {
  "type": "remote",
  "url": "https://play-mcp.example.com/mcp",
  "headers": { "Authorization": "Bearer {env:GPSCRAPE_MCP_TOKEN}" }
}
```

## Tools

| Tool | What it returns | Default / max |
|---|---|---|
| `play_search_apps` | apps matching keywords, with package ids | 20 / 50 |
| `play_get_app` | one app's store page, including whether it can be installed in that country; `response_format: detailed` adds description, histogram, developer contacts | — |
| `play_get_apps` | concise details for several package ids in one request | up to 20 ids |
| `play_get_chart` | a chart: `top_free`, `top_paid`, `grossing`, `new_free`, `new_paid`, `movers_shakers` | 50 / 200 |
| `play_get_reviews` | one page of reviews; `next_token` for the next page; filter by star rating | 20 / 100 |
| `play_get_similar_apps` | apps the store lists as similar | 20 / 100 |
| `play_get_developer_apps` | apps by one developer | 20 / 100 |
| `play_get_permissions` | Android permissions, grouped | — |
| `play_get_data_safety` | declared data collection, sharing and security practices | — |
| `play_suggest_queries` | autocomplete suggestions for a query | — |
| `play_list_categories` | category ids accepted by `play_get_chart` | — |
| `play_check_availability` | per country: `available`, `region_locked` (listed, not installable) or `not_listed` | up to 50 countries |

Tools take `lang` and `country` (default `en`, `us`), with three exceptions:
`play_get_reviews` takes only `lang`, `play_check_availability` takes a list
of `countries`, and `play_list_categories` takes nothing. An unknown country code or a malformed language
code is an error, because the store would silently answer with its defaults.
In `play_get_reviews`, `lang` selects the language group of the reviews (Google's
grouping, so a few may be in other languages); reviews are the same in every
country. Prices, charts and installability depend on `country`.

`play_search_apps`, `play_get_chart`, `play_get_similar_apps` and
`play_get_developer_apps` fetch full details for their
results (one extra batched request per 32 apps), because the store's result
rows carry no developer id, and outside charts no price.

### Design notes

- **Names say what they do.** `play_` + verb + object, as Anthropic's tool
  guidance (namespacing) and OpenAI's (action-oriented names) both ask; the
  prefix also keeps them apart from another store's tools in one session.
- **Small answers by default.** Lists come back as one-line summaries.
  `play_get_reviews` cuts each text to 400 characters unless you ask for `detailed`,
  and says how many it returned, which filters applied and whether anything
  was cut.
- **Schemas both ways.** Input schemas carry `minimum`/`maximum`, `enum`
  and array lengths, so a client can check an argument before calling; an
  omitted number takes its default, an explicit `0` is out of range. Every
  tool also publishes an output schema, as OpenAI's MCP guidance asks for
  structured results, and the server validates each result against it
  before sending. That roughly doubles `tools/list` (about 17.5 KB, from
  9.8 KB); clients with tool search load definitions on demand and pay it
  only for the tools they use. Results come back as `structuredContent`
  and as the same JSON in `content`, because some clients (the OpenAI Agents
  SDK by default) show the model only `content`.
- **Ids as they come.** `app_id` takes a package id or a store link with
  `?id=`.
- **Third-party text is marked.** Review text, reviewer names, developer
  replies, app summaries, descriptions and release notes come back in fields
  named `untrusted_*`, and the server's instructions tell the agent to treat
  them as data, not instructions.
- **Errors say what to do next, behind a stable code.** Every tool error
  reads `code: what to do`. A name instead of a package id points to
  `play_search_apps`; an unknown category points to `play_list_categories`
  before any request is made; a 404 says to check the id or the country; a
  block says to wait; a bug that panics returns an error instead of ending
  the session. The codes:

  | Code | Meaning | Retry? |
  |---|---|---|
  | `invalid_argument` | fix the arguments | no |
  | `not_found` | no such app or page in this storefront | another id or country |
  | `not_listed` | the store returned nothing for this id here | once, then check the id |
  | `rate_limited` | the store is refusing this server for now | after a few minutes |
  | `timeout` | the store did not answer in time | once |
  | `busy` | this server is at capacity | shortly |
  | `cancelled` | the client cancelled the call | — |
  | `store_error` | any other failure talking to the store | once |
  | `internal` | a bug in gpscrape-mcp | no; report it |

  Arguments that break the input schema are rejected by the MCP SDK before
  the tool runs; those errors start with `validating "arguments"`.
- **Gentle on the store, bounded on the server.** Requests to Google Play are
  throttled and retried honouring `Retry-After`. Tool calls run at most
  `-max-concurrent` at once, at most four times that many wait for a slot,
  and any further call is refused at once as `busy`. String arguments have a
  maximum length in the schema. `play_check_availability` gets twice the
  `-timeout`.
- **Progress for the slow tool.** `play_check_availability` makes one store
  request per country and can take two minutes for fifty. A client that sends
  a `progressToken` gets a notification per country over stdio (the stateless
  HTTP transport answers with one JSON body and has no stream to send them
  on).
- **Cancelled means cancelled.** A call the client cancelled returns no
  result, even if the work behind it finished.
- **Cacheable catalog.** `tools/list` and `server/discover` say `ttlMs` of an
  hour: the tool set never changes while the server runs.

## Flags

```
-http addr          serve Streamable HTTP instead of stdio
-token-file path    read the bearer token from a file (default $GPSCRAPE_MCP_TOKEN)
-secret-path        also accept the token as /mcp/<token>, for ChatGPT
-throttle 300ms     minimum interval between requests to Google Play
-max-concurrent 4   tool calls running at once
-timeout 60s        time limit for one tool call
-v                  log every tool call to stderr
-version            print the version
```

Logs go to stderr only. In stdio mode stdout carries the protocol.

## Conformance

CI runs the official [MCP conformance suite](https://github.com/modelcontextprotocol/conformance)
against the HTTP server. Most of its server scenarios call fixtures of the
reference test server (`test_simple_text`, `test_sampling`, test prompts and
resources) or features this server does not offer (completion, resource
subscriptions, sessions); [`conformance-baseline.yml`](conformance-baseline.yml)
lists them with the reason. Everything else must pass. To run it locally:

```bash
gpscrape-mcp -http 127.0.0.1:3001 &
npx -y @modelcontextprotocol/conformance@0.1.16 server \
  --url http://127.0.0.1:3001/mcp --expected-failures conformance-baseline.yml
```
