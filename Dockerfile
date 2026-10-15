# syntax=docker/dockerfile:1

# The release image: infraharvest, pinned Terraform and OpenTofu binaries,
# and git, which Terraform needs to fetch registry modules hosted on GitHub.
# GoReleaser builds infraharvest and puts it in the build context under
# $TARGETPLATFORM/ (see .goreleaser.yaml); nothing is compiled here.
#
# Terraform and OpenTofu download in stages of their own, on the build
# machine: BuildKit runs them in parallel, and bumping one keeps the other
# cached. The engines workflow checks every night for newer releases.

ARG TERRAFORM_VERSION=1.16.6
ARG TOFU_VERSION=1.13.1

FROM --platform=$BUILDPLATFORM alpine:3.24@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6 AS fetch
# pipefail: a pipe fails when any of its commands does, not only the last.
SHELL ["/bin/ash", "-eo", "pipefail", "-c"]
RUN mkdir /out /work

FROM fetch AS terraform
ARG TARGETARCH
ARG TERRAFORM_VERSION
# SHA-256 of the release archives, from the release's SHA256SUMS file.
ARG TERRAFORM_SHA256_amd64=38034b38d03096399a7a1264a14561a8029b2b496fd1f169452656e53968f1b7
ARG TERRAFORM_SHA256_arm64=a750b5a11038f165846b2658c40c88cd7070aa67a61bab431a14ba27928ea9ea
RUN case "$TARGETARCH" in \
      amd64) sum="$TERRAFORM_SHA256_amd64" ;; \
      arm64) sum="$TERRAFORM_SHA256_arm64" ;; \
      *) echo "unsupported architecture $TARGETARCH" >&2; exit 1 ;; \
    esac; \
    wget -q -O /tmp/terraform.zip "https://releases.hashicorp.com/terraform/${TERRAFORM_VERSION}/terraform_${TERRAFORM_VERSION}_linux_${TARGETARCH}.zip"; \
    echo "$sum  /tmp/terraform.zip" | sha256sum -c -; \
    unzip -q /tmp/terraform.zip terraform -d /out

FROM fetch AS tofu
ARG TARGETARCH
ARG TOFU_VERSION
ARG TOFU_SHA256_amd64=378ada19d4bc70c43732004e8159be771b23b9a5afdf059e5f8a2b3fa2c70a69
ARG TOFU_SHA256_arm64=9c1ef375aa1852db0b2888aa921b640c71f8140d4682aa4fec99378a64fa7dc3
RUN case "$TARGETARCH" in \
      amd64) sum="$TOFU_SHA256_amd64" ;; \
      arm64) sum="$TOFU_SHA256_arm64" ;; \
      *) echo "unsupported architecture $TARGETARCH" >&2; exit 1 ;; \
    esac; \
    wget -q -O /tmp/tofu.tar.gz "https://github.com/opentofu/opentofu/releases/download/v${TOFU_VERSION}/tofu_${TOFU_VERSION}_linux_${TARGETARCH}.tar.gz"; \
    echo "$sum  /tmp/tofu.tar.gz" | sha256sum -c -; \
    tar -xzf /tmp/tofu.tar.gz -C /out tofu

# Non-root, with CA certificates and git. The layers change least often
# first: the engines, then infraharvest itself.
FROM cgr.dev/chainguard/git:latest@sha256:c40e62924ad09836826b9edf4bd56d9c6ae98e1ab387623775db1b64e959b33c
ARG TARGETPLATFORM
COPY --link --from=terraform /out/terraform /usr/local/bin/terraform
COPY --link --from=tofu /out/tofu /usr/local/bin/tofu
COPY --link --chown=65532:65532 --from=fetch /work /work
COPY --link $TARGETPLATFORM/infraharvest /usr/local/bin/infraharvest
# CHECKPOINT_DISABLE: Terraform doesn't call HashiCorp's version-check
# service on every run.
ENV HOME=/home/git \
    CHECKPOINT_DISABLE=1
USER 65532
WORKDIR /work
ENTRYPOINT ["/usr/local/bin/infraharvest"]
