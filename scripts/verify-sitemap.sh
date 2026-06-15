#!/usr/bin/env bash
# Empirical verification of the sitemap full-catalog feature (EnumerateCatalog
# and friends). Three independent layers, so a failure localizes the cause:
#   Part A — raw Google endpoints via curl/gunzip/grep (the store structure is
#            as the code assumes; curl cannot lie about what Google serves).
#   Part B — the Go implementation LIVE, via the committed canary test.
#   Part C — the Go implementation OFFLINE, via the unit tests (-race).
#
# If Google ever changes its sitemap layout, Part A turns red first — separately
# from the Go code — pointing at "their structure moved" rather than "our parse
# broke". Requires network for A and B; C is offline.
#
# Usage: scripts/verify-sitemap.sh   (exit 0 = all pass, 1 = any fail)
set -uo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BASE="https://play.google.com"
UA="Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/120.0.0.0 Safari/537.36"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

pass=0; fail=0
ok()  { echo "PASS  $1 — $2"; pass=$((pass+1)); }
bad() { echo "FAIL  $1 — $2"; fail=$((fail+1)); }

echo "=============================================================="
echo "PART A — raw Google sitemap endpoints (curl)"
echo "=============================================================="

# A1: robots.txt advertises >=2 sitemap indexes, all under /sitemaps/
mapfile -t SITEMAPS < <(curl -sA "$UA" "$BASE/robots.txt" | awk -F': ' 'tolower($1)=="sitemap"{print $2}' | tr -d '\r')
n_idx=${#SITEMAPS[@]}
under=0
for u in "${SITEMAPS[@]}"; do [[ "$u" == "$BASE/sitemaps/"* ]] && under=$((under+1)); done
if (( n_idx >= 2 && under == n_idx )); then
  ok "robots.txt indexes" "$n_idx found, all under /sitemaps/: ${SITEMAPS[*]}"
else
  bad "robots.txt indexes" "$n_idx found, $under under /sitemaps/"
fi

# A2: index-0 lists >1000 .xml.gz shards
IDX0="${SITEMAPS[0]:-$BASE/sitemaps/sitemaps-index-0.xml}"
curl -sA "$UA" "$IDX0" -o "$TMP/idx0.xml"
n_shards=$(grep -o '\.xml\.gz' "$TMP/idx0.xml" | wc -l | tr -d ' ')
FIRST_SHARD=$(grep -o 'https://[^<]*\.xml\.gz' "$TMP/idx0.xml" | head -1)
if (( n_shards > 1000 )) && [[ -n "$FIRST_SHARD" ]]; then
  ok "index-0 shard list" "$n_shards shards, first=$(basename "$FIRST_SHARD")"
else
  bad "index-0 shard list" "$n_shards shards, first=$FIRST_SHARD"
fi

# A3: a real shard is gzip, decompresses, and carries app-detail locs
curl -sA "$UA" "$FIRST_SHARD" -o "$TMP/shard0.gz"
is_gzip=$(file "$TMP/shard0.gz" | grep -c gzip)
mapfile -t APPS < <(gunzip -c "$TMP/shard0.gz" 2>/dev/null | grep -o 'store/apps/details?id=[a-zA-Z0-9._]*' | sed 's/.*id=//' | sort -u)
n_apps=${#APPS[@]}
total_urls=$(gunzip -c "$TMP/shard0.gz" 2>/dev/null | grep -o '<loc>' | wc -l | tr -d ' ')
if (( is_gzip == 1 && n_apps > 0 )); then
  ok "shard 0 app ids" "$n_apps apps of $total_urls urls, e.g. ${APPS[0]}"
else
  bad "shard 0 app ids" "gzip=$is_gzip apps=$n_apps"
fi

# A4: a real app id from the shard resolves to a live listing (200)
SAMPLE="${APPS[0]:-}"
code=$(curl -sA "$UA" -o /dev/null -w '%{http_code}' "$BASE/store/apps/details?id=$SAMPLE&hl=en")
if [[ "$code" == "200" ]]; then
  ok "sample id is a real listing" "$SAMPLE -> HTTP $code"
else
  bad "sample id is a real listing" "$SAMPLE -> HTTP $code"
fi

# A5: a bogus shard URL returns 404 (the error path the Go code surfaces)
code=$(curl -sA "$UA" -o /dev/null -w '%{http_code}' "$BASE/sitemaps/does-not-exist-xyz.xml.gz")
if [[ "$code" == "404" ]]; then
  ok "bogus shard 404" "HTTP $code"
else
  bad "bogus shard 404" "expected 404, got $code"
fi

echo ""
echo "=============================================================="
echo "PART B — Go implementation LIVE (canary test)"
echo "=============================================================="
if (cd "$REPO" && go test -tags canary -run 'TestCanary/Sitemap' . >"$TMP/canary.log" 2>&1); then
  ok "TestCanary/Sitemap" "$(grep -E '^ok' "$TMP/canary.log" | head -1)"
else
  bad "TestCanary/Sitemap" "see log below"; cat "$TMP/canary.log"
fi

echo ""
echo "=============================================================="
echo "PART C — Go implementation OFFLINE (unit tests, -race)"
echo "=============================================================="
if (cd "$REPO" && go test -short -race -run 'Sitemap|Catalog|AppPackage|Gunzip' . >"$TMP/unit.log" 2>&1); then
  ok "offline unit tests" "$(tail -1 "$TMP/unit.log")"
else
  bad "offline unit tests" "see log"; cat "$TMP/unit.log"
fi

echo ""
echo "=============================================================="
echo "RESULT: $pass PASS, $fail FAIL"
echo "=============================================================="
exit $(( fail > 0 ? 1 : 0 ))
