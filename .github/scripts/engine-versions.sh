#!/usr/bin/env bash
# Copyright 2026 Gabriel Ignat
# SPDX-License-Identifier: AGPL-3.0-only

# Prints terraform=<version> and tofu=<version>: the tags of the Dockerfile's
# hashicorp/terraform and opentofu/opentofu images, which Dependabot bumps.
# The workflows install these versions (the setup-engines action, the
# adapters workflow), so the Dockerfile is the one place they are set.
#
# Usage: engine-versions.sh >> "$GITHUB_OUTPUT" (from the repository's root)
set -euo pipefail

terraform=$(sed -nE 's/^FROM hashicorp\/terraform:([0-9][^@ ]*).*/\1/p' Dockerfile)
tofu=$(sed -nE 's/^FROM ghcr\.io\/opentofu\/opentofu:([0-9][^-@ ]*).*/\1/p' Dockerfile)
if [ -z "$terraform" ] || [ -z "$tofu" ]; then
  echo "::error::Dockerfile: no hashicorp/terraform or opentofu/opentofu tag found" >&2
  exit 1
fi
echo "terraform=$terraform"
echo "tofu=$tofu"
