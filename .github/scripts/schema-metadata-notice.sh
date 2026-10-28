#!/usr/bin/env bash
# Copyright 2026 Gabriel Ignat
# SPDX-License-Identifier: AGPL-3.0-only

# Writes the repository's NOTICE to standard output: infraharvest's licence,
# and for each provider's gzipped schema metadata in <dir>
# (<provider>.json.gz), the MPL-2.0 work it was read from, at the release
# the file records, and where that work's source is, as MPL-2.0 section 3.2
# asks of what is distributed. The schema-metadata workflows write it with
# the files, and a test of the schemameta package checks that it names the
# releases they record. It ends with OPA's capabilities file, which
# internal/engine embeds under Apache-2.0, as section 4 of that licence
# asks.
#
# Usage: schema-metadata-notice.sh <dir>
set -euo pipefail

dir=${1:?usage: $0 <dir>}

cat <<'EOF'
infraharvest
Copyright 2026 Gabriel Ignat

infraharvest is licensed under the GNU Affero General Public License,
version 3 only (AGPL-3.0-only): see LICENSE.

It embeds data read out of the following works, which are licensed under
the Mozilla Public License, version 2.0 (MPL-2.0): see LICENSES/MPL-2.0.txt
or https://mozilla.org/MPL/2.0/. The data is the arguments' defaults and
the constraints between them that each provider's resource schemas
declare, at the release named, and it stays under MPL-2.0; the rest of
infraharvest is a larger work under AGPL-3.0-only (MPL-2.0 section 3.3).
EOF
for path in "$dir"/*.json.gz; do
  provider=$(basename "$path" .json.gz)
  # sed reads to the end: head would stop gzip with SIGPIPE, which
  # pipefail reports.
  release=$(gzip -dc "$path" | sed -n '1s/.*"version":"\([^"]*\)".*/\1/p')
  echo
  echo "internal/catalogue/schemameta/$provider.json.gz"
  echo "  from terraform-provider-$provider $release, Copyright IBM Corp. and HashiCorp, Inc."
  echo "  source: https://github.com/hashicorp/terraform-provider-$provider/tree/v$release"
done
cat <<'EOF'

These files are the data's Source Code Form, one resource type to a line,
gzipped: `gzip -dc <file>` reads one. They are in infraharvest's source at
the same release, at the paths above.

It also embeds the following file of the Open Policy Agent (OPA), which is
licensed under the Apache License, version 2.0 (Apache-2.0): see
LICENSES/Apache-2.0.txt or https://www.apache.org/licenses/LICENSE-2.0.
It lists OPA's built-in functions, keywords and features, and infraharvest
gives conftest those of them it lets policies use; the file is unchanged
and stays under Apache-2.0.

internal/engine/conftest_capabilities.json
  from OPA 1.21.1 (capabilities/v1.21.1.json), Copyright The OPA Authors
  source: https://github.com/open-policy-agent/opa/blob/v1.21.1/capabilities/v1.21.1.json
EOF
