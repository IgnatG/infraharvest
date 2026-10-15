# Install

infraharvest needs Terraform or OpenTofu to generate configuration. With the default `--engine=terraform`, it uses the Terraform on `PATH` if it is 1.5 or later, or else downloads the latest release and verifies it. `--engine=tofu` needs OpenTofu 1.6 or later installed. `--terraform-path` names a binary to use instead. Terraform downloads the providers itself, so there is nothing else to install.

## From a release

Each [release](https://github.com/IgnatG/infraharvest/releases) has archives for Linux, macOS and Windows on amd64 and arm64. It also has a checksum file, signed with [cosign](https://github.com/sigstore/cosign) from the release workflow (keyless, no long-lived key), SBOMs and build provenance. To verify a download, check the checksum file's signature, then the archive's checksum:

```sh
VERSION=0.2.0
cosign verify-blob \
  --certificate-identity "https://github.com/IgnatG/infraharvest/.github/workflows/release.yml@refs/heads/main" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --bundle "infraharvest_${VERSION}_SHA256SUMS.sigstore.json" "infraharvest_${VERSION}_SHA256SUMS"
sha256sum --check --ignore-missing "infraharvest_${VERSION}_SHA256SUMS"
```

## Container image

`ghcr.io/ignatg/infraharvest` (linux/amd64 and linux/arm64, signed with cosign) includes pinned Terraform and OpenTofu binaries, and git for registry modules. It runs as a non-root user in `/work`:

```sh
docker run --rm -v "$PWD:/work" -v "$HOME/.aws:/home/git/.aws:ro" -e AWS_PROFILE \
  ghcr.io/ignatg/infraharvest import aws --all --services=vpc --regions=eu-west-2
```

## With the install script

For Linux and macOS, including AWS CloudShell, Azure Cloud Shell and Google Cloud Shell. It checks the archive's checksum and the release's signature, so it needs [cosign](https://docs.sigstore.dev/cosign/system_config/installation/) installed. Set `INFRAHARVEST_SKIP_SIGNATURE=1` to install with the checksum check only:

```sh
curl -fsSL https://raw.githubusercontent.com/IgnatG/infraharvest/main/install.sh | sh
```

## With Go

`go install github.com/IgnatG/infraharvest@latest`

## From source

With the Go release `go.mod` names (Go fetches it if yours is older and `GOTOOLCHAIN` allows):

```sh
git clone https://github.com/IgnatG/infraharvest.git
cd infraharvest
go build -o infraharvest .
```

A plain build has every provider. The module brings in three clouds' SDKs, so compiling all of it takes several GB of memory. To build only the providers you use, list them after the `minimal` tag (`aws`, `azure`, `google`):

```sh
go build -tags minimal,aws -o infraharvest .          # AWS only
go build -tags minimal,aws,google -o infraharvest .   # AWS and Google Cloud
```

The other providers' commands are then missing from `import` and `discover`.
