# syntax=docker/dockerfile:1

# Copyright 2026 Gabriel Ignat
# SPDX-License-Identifier: AGPL-3.0-only

# The release image: infraharvest, pinned Terraform and OpenTofu binaries,
# and git, which Terraform needs to fetch registry modules hosted on GitHub.
# GoReleaser builds infraharvest and puts it in the build context under
# $TARGETPLATFORM/ (see .goreleaser.yaml); nothing is compiled here. There
# is one image per cloud, and the binary in the context is that cloud's
# build (-tags minimal,<cloud>): CLOUD, passed by .goreleaser.yaml, names it
# in a label.
#
# Terraform and OpenTofu come from their official images, pinned by tag and
# digest, so Dependabot proposes their new releases like any other image.
# Only their binaries (and Terraform's licence) are copied.

FROM hashicorp/terraform:1.17.2@sha256:6e9c1e647c31a960dc9b59e13272f9111d90010c10b717f77ffb324767660475 AS terraform

FROM ghcr.io/opentofu/opentofu:1.13.10-minimal@sha256:5c6cef27227e50060139fec7122c83d14088b5dd2677162545f0e699e1f6196c AS tofu

# The working directory, owned by the image's non-root user.
FROM --platform=$BUILDPLATFORM alpine:3.24@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf AS workdir
RUN mkdir /work

# Non-root, with CA certificates and git. The layers change least often
# first: the engines, then infraharvest itself.
FROM cgr.dev/chainguard/git:latest@sha256:4bf593d67b62c4c8f9252af29e9a39c0e03cebacab966068ffc1a0672b186009
ARG TARGETPLATFORM
# aws, azure or google.
ARG CLOUD
LABEL io.github.ignatg.infraharvest.cloud=$CLOUD
COPY --link --from=terraform /bin/terraform /usr/local/bin/terraform
COPY --link --from=terraform /usr/share/doc/terraform/LICENSE.txt /usr/share/doc/terraform/LICENSE.txt
COPY --link --from=tofu /usr/local/bin/tofu /usr/local/bin/tofu
COPY --link --chown=65532:65532 --from=workdir /work /work
COPY --link $TARGETPLATFORM/infraharvest /usr/local/bin/infraharvest
# infraharvest's licence, and the NOTICE and licences of the MPL-2.0 and
# Apache-2.0 data it embeds.
COPY --link LICENSE NOTICE LICENSES/MPL-2.0.txt LICENSES/Apache-2.0.txt /usr/share/doc/infraharvest/
# CHECKPOINT_DISABLE: Terraform doesn't call HashiCorp's version-check
# service on every run.
ENV HOME=/home/git \
    CHECKPOINT_DISABLE=1
USER 65532
WORKDIR /work
ENTRYPOINT ["/usr/local/bin/infraharvest"]
