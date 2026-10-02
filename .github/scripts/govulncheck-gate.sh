#!/usr/bin/env bash
# Fails when the binary contains a vulnerable symbol that is not covered by
# .github/govulncheck-allowlist.txt (by OSV ID or module path).
# Usage: govulncheck-gate.sh <binary>
set -euo pipefail

binary=$1
allowlist=.github/govulncheck-allowlist.txt

go run golang.org/x/vuln/cmd/govulncheck@v1.8.0 -mode binary -format json "$binary" > govulncheck.json

# "<osv id> <module>" for each vulnerable function found in the binary.
found=$(jq -r 'select(.finding.trace[0].function? != null) | "\(.finding.osv) \(.finding.trace[0].module)"' govulncheck.json | sort -u)
allowed=$(grep -vE '^[[:space:]]*(#|$)' "$allowlist" | awk '{print $1}')

blocked=""
while read -r id module; do
  [ -n "$id" ] || continue
  if ! grep -qxF -e "$id" -e "$module" <<< "$allowed"; then
    blocked+="  $id in $module"$'\n'
  fi
done <<< "$found"

for entry in $allowed; do
  if [ -z "$(awk -v e="$entry" '$1 == e || $2 == e' <<< "$found")" ]; then
    echo "::warning::govulncheck allowlist entry '$entry' no longer matches anything; remove it"
  fi
done

if [ -n "$blocked" ]; then
  echo "::error::Vulnerabilities not covered by $allowlist:"
  printf '%s' "$blocked"
  echo "Run 'go run golang.org/x/vuln/cmd/govulncheck@v1.8.0 -mode binary $binary' for details."
  exit 1
fi
echo "No vulnerabilities outside the allowlist."
