#!/usr/bin/env bash
# Run govulncheck and fail on any *called* vulnerability, except IDs explicitly
# allowlisted below. The allowlist is for vulnerabilities in transitive
# dependencies that have no fixed upstream release yet — each MUST be tracked and
# removed the moment a fix ships. govulncheck already filters to vulns that are
# actually reachable from this module's code, so a finding here is a real call
# path, not just a vulnerable version in the graph.
set -uo pipefail

# OSV IDs to tolerate. Keep this list short and annotated.
ALLOW=(
  "GO-2026-4479" # pion/dtls/v2 v2.2.12 (via pion/ice) — no fixed version published upstream
)

if ! command -v govulncheck >/dev/null 2>&1; then
  echo "govulncheck not found; install with: go install golang.org/x/vuln/cmd/govulncheck@latest" >&2
  exit 2
fi

json="$(govulncheck -format json ./... 2>/dev/null)"

# A finding whose trace's innermost frame has a function is a *called* vuln (as
# opposed to an imported-but-unreachable one). Collect the unique OSV IDs.
called="$(printf '%s' "$json" | jq -r 'select(.finding.trace[0].function != null) | .finding.osv' | sort -u)"

fail=0
while IFS= read -r id; do
  [[ -z "$id" ]] && continue
  allowed=0
  for a in "${ALLOW[@]}"; do
    [[ "$id" == "$a" ]] && allowed=1
  done
  if [[ "$allowed" == 1 ]]; then
    echo "govulncheck: allowlisted $id (no upstream fix; tracked)"
  else
    echo "::error::govulncheck: non-allowlisted called vulnerability $id"
    fail=1
  fi
done <<<"$called"

if [[ "$fail" == 0 ]]; then
  echo "govulncheck: no non-allowlisted called vulnerabilities"
fi
exit "$fail"
