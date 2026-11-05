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
left out on purpose and stay in the [`gpscrape`](../README.md) command.

## Install

```bash
go install github.com/kryuchenko/google-play-scraper/mcp/cmd/gpscrape-mcp@latest
```

Requires Go 1.27.

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

**ChatGPT** cannot start a local process and cannot send a token: its custom
apps take OAuth or nothing. Host the server yourself (a VPS, or your own
machine behind a tunnel such as Cloudflare Tunnel) with HTTPS in front, and
give ChatGPT the token as the last part of the URL:

```
https://play-mcp.example.com/mcp/<GPSCRAPE_MCP_TOKEN>
```

Add it in ChatGPT as a custom app (developer mode) with
**No authentication**: the secret path is the password. Any other path under
`/mcp/` answers 404. Use a long random token (`openssl rand -hex 32`); the
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
| `search` | apps matching keywords, with package ids | 20 / 50 |
| `app` | one app's store page, including whether it can be installed in that country; `response_format: detailed` adds description, histogram, developer contacts | — |
| `apps` | concise details for several package ids in one request | up to 20 ids |
| `list` | a chart: `top_free`, `top_paid`, `grossing`, `new_free`, `new_paid`, `movers_shakers` | 50 / 200 |
| `reviews` | one page of reviews; `next_token` for the next page; filter by star rating | 20 / 100 |
| `similar` | apps the store lists as similar | 20 / 100 |
| `developer` | apps by one developer | 20 / 100 |
| `permissions` | Android permissions, grouped | — |
| `datasafety` | declared data collection, sharing and security practices | — |
| `suggest` | autocomplete suggestions for a query | — |
| `categories` | category ids accepted by `list` | — |
| `availability` | per country: `available`, `region_locked` (listed, not installable) or `not_listed` | up to 50 countries |

Tools take `lang` and `country` (default `en`, `us`), with three exceptions:
`reviews` takes only `lang`, `availability` takes a list of `countries`, and
`categories` takes nothing. An unknown country code or a malformed language
code is an error, because the store would silently answer with its defaults.
In `reviews`, `lang` selects the language group of the reviews (Google's
grouping, so a few may be in other languages); reviews are the same in every
country. Prices, charts and installability depend on `country`.

`search`, `list`, `similar` and `developer` fetch full details for their
results (one extra batched request per 32 apps), because the store's result
rows carry no developer id, and outside charts no price.

### Design notes

- **Small answers by default.** Lists come back as one-line summaries.
  `reviews` cuts each text to 400 characters unless you ask for `detailed`,
  and says how many it returned, which filters applied and whether anything
  was cut.
- **Bounds in the schema, no output schemas.** Input schemas carry
  `minimum`/`maximum`, `enum` and array lengths, so a client can check an
  argument before calling; an omitted number takes its default, an explicit
  `0` is out of range. Output schemas are left out to keep `tools/list`
  small (about 9.5 KB), since every client loads it before the first
  question. Results come back as `structuredContent` and as the same JSON in
  `content`, because some clients (the OpenAI Agents SDK by default) show the
  model only `content`.
- **Ids as they come.** `app_id` takes a package id or a store link with
  `?id=`.
- **Third-party text is marked.** Review text, reviewer names, developer
  replies, app summaries, descriptions and release notes come back in fields
  named `untrusted_*`, and the server's instructions tell the agent to treat
  them as data, not instructions.
- **Errors say what to do next.** A name instead of a package id points to
  `search`; an unknown category points to `categories` before any request is
  made; a 404 says to check the id or the country; a block says to wait. A
  call that waits too long for a free slot is told the server is busy; a bug
  that panics returns an error instead of ending the session.
- **Gentle on the store.** Requests to Google Play are throttled and retried
  honouring `Retry-After`, and tool calls run at most `-max-concurrent` at
  once (see [Flags](#flags)). `availability` gets twice the `-timeout`.

## Flags

```
-http addr          serve Streamable HTTP instead of stdio
-token string       bearer token for /mcp (default $GPSCRAPE_MCP_TOKEN)
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
