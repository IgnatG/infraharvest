# syntax=docker/dockerfile:1

# Copyright 2026 Gabriel Ignat
# SPDX-License-Identifier: AGPL-3.0-only

# The release image: infraharvest, pinned Terraform and OpenTofu binaries,
# and git, which Terraform needs to fetch registry modules hosted on GitHub.
# GoReleaser builds infraharvest and puts it in the build context under
# $TARGETPLATFORM/ (see .goreleaser.yaml); nothing is compiled here.
#
# Terraform and OpenTofu come from their official images, pinned by tag and
# digest, so Dependabot proposes their new releases like any other image.
# Only their binaries (and Terraform's licence) are copied.

FROM hashicorp/terraform:1.16.6@sha256:bfffc1ded5fbf0a17beaaf22eb4793980abdb19d0761ff6a3a0efb2ab4d9b316 AS terraform

FROM ghcr.io/opentofu/opentofu:1.13.2-minimal@sha256:622dc6bb98b105c18f53cf419ab64a4f3b88bd9f8c16cc978cf89e4202f4613f AS tofu

# The working directory, owned by the image's non-root user.
FROM --platform=$BUILDPLATFORM alpine:3.24@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6 AS workdir
RUN mkdir /work

# Non-root, with CA certificates and git. The layers change least often
# first: the engines, then infraharvest itself.
FROM cgr.dev/chainguard/git:latest@sha256:913be6e43cb65ed65bc5c5f9b22e68e780616a66c5237da59334c7d1cdc3d628
ARG TARGETPLATFORM
COPY --link --from=terraform /bin/terraform /usr/local/bin/terraform
COPY --link --from=terraform /usr/share/doc/terraform/LICENSE.txt /usr/share/doc/terraform/LICENSE.txt
COPY --link --from=tofu /usr/local/bin/tofu /usr/local/bin/tofu
COPY --link --chown=65532:65532 --from=workdir /work /work
COPY --link $TARGETPLATFORM/infraharvest /usr/local/bin/infraharvest
# infraharvest's licence, and the NOTICE and licence of the MPL-2.0 data
# it embeds.
COPY --link LICENSE NOTICE LICENSES/MPL-2.0.txt /usr/share/doc/infraharvest/
# CHECKPOINT_DISABLE: Terraform doesn't call HashiCorp's version-check
# service on every run.
ENV HOME=/home/git \
    CHECKPOINT_DISABLE=1
USER 65532
WORKDIR /work
ENTRYPOINT ["/usr/local/bin/infraharvest"]
