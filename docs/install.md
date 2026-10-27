# Install

infraharvest needs Terraform or OpenTofu to generate configuration. With the default `--engine=terraform`, it uses the Terraform on `PATH` if it is 1.5 or later, or else downloads the latest release and verifies it. `--engine=tofu` needs OpenTofu 1.6 or later installed. `--terraform-path` names a binary to use instead. Terraform downloads the providers itself, so there is nothing else to install.

## From a release

Each [release](https://github.com/IgnatG/infraharvest/releases) has archives for Linux, macOS and Windows on amd64 and arm64. It also has a checksum file, signed with [cosign](https://github.com/sigstore/cosign) from the release workflow (keyless, no long-lived key), SBOMs and build provenance. To verify a download, check the checksum file's signature, then the archive's checksum:

```sh
VERSION=0.1.0
cosign verify-blob \
  --certificate-identity "https://github.com/IgnatG/infraharvest/.github/workflows/release.yml@refs/heads/main" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --bundle "infraharvest_${VERSION}_SHA256SUMS.sigstore.json" "infraharvest_${VERSION}_SHA256SUMS"
sha256sum --check --ignore-missing "infraharvest_${VERSION}_SHA256SUMS"
```

The archives' build provenance is attested on GitHub; with the [GitHub CLI](https://cli.github.com/), `gh attestation verify "infraharvest_${VERSION}_linux_amd64.tar.gz" --repo IgnatG/infraharvest` checks it.

Each archive is named after the release, the system and the architecture, such as `infraharvest_0.1.0_linux_amd64.tar.gz` or `infraharvest_0.1.0_windows_amd64.zip`, and holds the `infraharvest` binary (`infraharvest.exe` on Windows) with the licence and the README. Extract it into a directory on your `PATH`. On Windows, which the install script doesn't cover, this, the container image and Go are the ways to install.

## Container image

`ghcr.io/ignatg/infraharvest` (linux/amd64 and linux/arm64, signed with cosign) includes pinned Terraform and OpenTofu binaries, and git for registry modules. It runs as a non-root user in `/work`:

```sh
docker run --rm -v "$PWD:/work" -v "$HOME/.aws:/home/git/.aws:ro" -e AWS_PROFILE \
  ghcr.io/ignatg/infraharvest import aws --all --services=vpc --regions=eu-west-2
```

The release workflow signs each image the same way as the checksum file. To verify an image before running it:

```sh
cosign verify ghcr.io/ignatg/infraharvest:v0.1.0 \
  --certificate-identity "https://github.com/IgnatG/infraharvest/.github/workflows/release.yml@refs/heads/main" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

Each release is tagged `v<version>`; `latest` is the newest release that isn't a prerelease.

## With the install script

For Linux and macOS, including AWS CloudShell, Azure Cloud Shell and Google Cloud Shell. It downloads a release, checks the release's signature with [cosign](https://docs.sigstore.dev/cosign/system_config/installation/), checks the archive against the signed checksum file, and installs the binary in `~/.local/bin`:

```sh
curl -fsSL https://raw.githubusercontent.com/IgnatG/infraharvest/main/install.sh | sh
```

It installs the latest release: GitHub never counts a prerelease as the latest. To install a given release, prereleases included, name it: `curl -fsSL https://raw.githubusercontent.com/IgnatG/infraharvest/main/install.sh | INFRAHARVEST_VERSION=v0.1.0 sh`. `INFRAHARVEST_INSTALL_DIR` changes where the binary goes.

### In a cloud shell

AWS CloudShell, Azure Cloud Shell and Google Cloud Shell don't include cosign, so the script stops before installing anything. Install cosign from its releases into your home directory first (AWS CloudShell and Google Cloud Shell keep it between sessions; Azure Cloud Shell does when it has a storage account):

```sh
mkdir -p ~/.local/bin
curl -fsSLo ~/.local/bin/cosign "https://github.com/sigstore/cosign/releases/latest/download/cosign-linux-$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')"
chmod +x ~/.local/bin/cosign
export PATH="$HOME/.local/bin:$PATH"
curl -fsSL https://raw.githubusercontent.com/IgnatG/infraharvest/main/install.sh | sh
```

`INFRAHARVEST_SKIP_SIGNATURE=1` installs without cosign, checking only the archive's checksum:

```sh
curl -fsSL https://raw.githubusercontent.com/IgnatG/infraharvest/main/install.sh | INFRAHARVEST_SKIP_SIGNATURE=1 sh
```

> **Warning:** without the signature, nothing shows that the release workflow built the archive: the checksum file comes from the same place as the archive, so whoever could replace one could replace the other. Use it only where you accept that, and prefer installing cosign.

## With Go

`go install github.com/IgnatG/infraharvest@latest`, or `@v0.1.0` for a given release.

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

The other providers' commands are then missing from `import` and `discover`, and so is the way to read their clouds' state: `--managed-state` names the build tag a source needs, such as `-tags minimal,aws` for `s3://`. A configuration file's section of theirs, under `providers` or in an environment, is an error that names the build tag too, rather than going unread. HCP Terraform's state (`tfc://`) is read in every build.

## In GitHub Actions

The [GitHub Action](https://github.com/IgnatG/infraharvest/blob/main/action/README.md) installs a release, checks its signature and checksum, and runs an import: `uses: IgnatG/infraharvest/action@<commit SHA>` with `version: v0.1.0`.

## Check the install

`infraharvest version` prints the version, and `infraharvest services aws` (or `azure`, `google`) the services an import can name.
