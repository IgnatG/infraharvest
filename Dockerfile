# syntax=docker/dockerfile:1

# The release image: infraharvest, pinned Terraform and OpenTofu binaries,
# and git, which Terraform needs to fetch registry modules hosted on GitHub.
# GoReleaser builds infraharvest and puts it in the build context under
# $TARGETPLATFORM/ (see .goreleaser.yaml); nothing is compiled here.

# Runs on the build machine and downloads the target platform's binaries.
FROM --platform=$BUILDPLATFORM alpine:3.22@sha256:5291449c3df73caf6ed85e649dec1b9e818b39a5d8c871e97afc13e9cd5e8fa8 AS engines
ARG TARGETARCH
ARG TERRAFORM_VERSION=1.16.5
ARG TOFU_VERSION=1.13.1
# SHA-256 of the release archives, from each release's SHA256SUMS file.
ARG TERRAFORM_SHA256_amd64=2bc2fcfff033265c9e02ca0351f01794eb122f62a9b2a49a3294b9e49eaab5e4
ARG TERRAFORM_SHA256_arm64=61a50b00485ee4810cf20581ef080fc54d34d666e175c58d9a10501c65c1ccde
ARG TOFU_SHA256_amd64=378ada19d4bc70c43732004e8159be771b23b9a5afdf059e5f8a2b3fa2c70a69
ARG TOFU_SHA256_arm64=9c1ef375aa1852db0b2888aa921b640c71f8140d4682aa4fec99378a64fa7dc3
RUN set -eu; \
    case "$TARGETARCH" in \
      amd64) terraform_sum="$TERRAFORM_SHA256_amd64"; tofu_sum="$TOFU_SHA256_amd64" ;; \
      arm64) terraform_sum="$TERRAFORM_SHA256_arm64"; tofu_sum="$TOFU_SHA256_arm64" ;; \
      *) echo "unsupported architecture $TARGETARCH" >&2; exit 1 ;; \
    esac; \
    mkdir -p /out /work; \
    wget -q -O /tmp/terraform.zip "https://releases.hashicorp.com/terraform/${TERRAFORM_VERSION}/terraform_${TERRAFORM_VERSION}_linux_${TARGETARCH}.zip"; \
    echo "$terraform_sum  /tmp/terraform.zip" | sha256sum -c -; \
    unzip -q /tmp/terraform.zip terraform -d /out; \
    wget -q -O /tmp/tofu.tar.gz "https://github.com/opentofu/opentofu/releases/download/v${TOFU_VERSION}/tofu_${TOFU_VERSION}_linux_${TARGETARCH}.tar.gz"; \
    echo "$tofu_sum  /tmp/tofu.tar.gz" | sha256sum -c -; \
    tar -xzf /tmp/tofu.tar.gz -C /out tofu

# Non-root, with CA certificates and git.
FROM cgr.dev/chainguard/git:latest@sha256:beba52e2cd9f5000eed00a52a02ff301a6308ef08b8ce976a9ced8f90c1b8f61
ARG TARGETPLATFORM
COPY --from=engines /out/terraform /out/tofu /usr/local/bin/
COPY --from=engines --chown=65532:65532 /work /work
COPY $TARGETPLATFORM/infraharvest /usr/local/bin/infraharvest
ENV HOME=/home/git
USER 65532
WORKDIR /work
ENTRYPOINT ["/usr/local/bin/infraharvest"]
