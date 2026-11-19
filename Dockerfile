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

FROM hashicorp/terraform:1.17.1@sha256:a5d5a5a892c98f60fcbfa81eac2a40fc19e7b3fa7719bccae10ff6111d22524a AS terraform

FROM ghcr.io/opentofu/opentofu:1.13.9-minimal@sha256:84af0049634b7a7c455ab79c485818a8ef7f775cad3b2d42ce1c98ade720b5d1 AS tofu

# The working directory, owned by the image's non-root user.
FROM --platform=$BUILDPLATFORM alpine:3.24@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf AS workdir
RUN mkdir /work

# Non-root, with CA certificates and git. The layers change least often
# first: the engines, then infraharvest itself.
FROM cgr.dev/chainguard/git:latest@sha256:48aac7c117dbd387f8ac577937fce71a01af0150030f6beb7b2a53fbb5a34d97
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
