#!/bin/sh
# Installs infraharvest from a GitHub release, for example in a cloud shell:
#
#   curl -fsSL https://raw.githubusercontent.com/IgnatG/infraharvest/main/install.sh | sh
#
# It checks that this repository's release workflow signed the release's
# checksum file, with cosign, and the archive against that file. It needs
# cosign (https://docs.sigstore.dev/cosign/system_config/installation/)
# unless told to skip the signature. Settings, from the environment:
#
#   INFRAHARVEST_VERSION      release to install, such as v0.2.0 (default: latest)
#   INFRAHARVEST_INSTALL_DIR  where to put the binary (default: ~/.local/bin)
#   INFRAHARVEST_SKIP_SIGNATURE=1  don't verify the signature: check only the
#                             checksum, which comes from the same place as the
#                             archive, so it proves nothing about who built it
#   INFRAHARVEST_REQUIRE_SIGNATURE=1  the default; wins over SKIP_SIGNATURE
#   GITHUB_TOKEN              token for the GitHub API and downloads, if needed
#   INFRAHARVEST_BASE_URL     where the release files are (for testing)

set -eu

repo="IgnatG/infraharvest"
install_dir="${INFRAHARVEST_INSTALL_DIR:-$HOME/.local/bin}"

fail() {
	echo "install.sh: $*" >&2
	exit 1
}

# fetch URL FILE downloads URL to FILE, with GITHUB_TOKEN if set.
fetch() {
	if command -v curl > /dev/null 2>&1; then
		if [ -n "${GITHUB_TOKEN:-}" ]; then
			curl -fsSL -H "Authorization: Bearer $GITHUB_TOKEN" -o "$2" "$1"
		else
			curl -fsSL -o "$2" "$1"
		fi
	elif command -v wget > /dev/null 2>&1; then
		if [ -n "${GITHUB_TOKEN:-}" ]; then
			wget -q --header "Authorization: Bearer $GITHUB_TOKEN" -O "$2" "$1"
		else
			wget -q -O "$2" "$1"
		fi
	else
		fail "needs curl or wget"
	fi
}

case "$(uname -s)" in
	Linux) os=linux ;;
	Darwin) os=darwin ;;
	*) fail "unsupported system $(uname -s); on Windows, download the zip from https://github.com/$repo/releases" ;;
esac
case "$(uname -m)" in
	x86_64 | amd64) arch=amd64 ;;
	aarch64 | arm64) arch=arm64 ;;
	*) fail "unsupported architecture $(uname -m)" ;;
esac

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

version="${INFRAHARVEST_VERSION:-}"
if [ -z "$version" ]; then
	fetch "https://api.github.com/repos/$repo/releases/latest" "$tmp/latest.json"
	version=$(sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' "$tmp/latest.json" | head -n 1)
	[ -n "$version" ] || fail "couldn't find the latest release"
fi
case "$version" in
	v*) ;;
	*) version="v$version" ;;
esac
number="${version#v}"
base="${INFRAHARVEST_BASE_URL:-https://github.com/$repo/releases/download/$version}"
archive="infraharvest_${number}_${os}_${arch}.tar.gz"
sums="infraharvest_${number}_SHA256SUMS"

echo "Installing infraharvest $version ($os/$arch) into $install_dir"
fetch "$base/$archive" "$tmp/$archive"
fetch "$base/$sums" "$tmp/$sums"

if [ "${INFRAHARVEST_SKIP_SIGNATURE:-}" = 1 ] && [ "${INFRAHARVEST_REQUIRE_SIGNATURE:-}" != 1 ]; then
	echo "INFRAHARVEST_SKIP_SIGNATURE=1: not verifying the release's signature, checking the checksum only." >&2
elif command -v cosign > /dev/null 2>&1; then
	fetch "$base/$sums.sigstore.json" "$tmp/$sums.sigstore.json"
	cosign verify-blob \
		--certificate-identity "https://github.com/$repo/.github/workflows/release.yaml@refs/heads/main" \
		--certificate-oidc-issuer https://token.actions.githubusercontent.com \
		--bundle "$tmp/$sums.sigstore.json" "$tmp/$sums" > /dev/null 2>&1 ||
		fail "the checksum file's signature doesn't verify: not installing"
	echo "Verified the release's signature."
else
	fail "cosign isn't installed, so the release's signature can't be verified: not installing. Install cosign (https://docs.sigstore.dev/cosign/system_config/installation/), or set INFRAHARVEST_SKIP_SIGNATURE=1 to check only the checksum"
fi

expected=$(awk -v f="$archive" '$2 == f { print $1 }' "$tmp/$sums")
[ -n "$expected" ] || fail "$archive isn't in $sums"
if command -v sha256sum > /dev/null 2>&1; then
	actual=$(sha256sum "$tmp/$archive" | awk '{ print $1 }')
else
	actual=$(shasum -a 256 "$tmp/$archive" | awk '{ print $1 }')
fi
[ "$actual" = "$expected" ] || fail "$archive doesn't match its checksum: not installing"

tar -xzf "$tmp/$archive" -C "$tmp" infraharvest
mkdir -p "$install_dir"
mv "$tmp/infraharvest" "$install_dir/infraharvest"
chmod 755 "$install_dir/infraharvest"
echo "Installed $("$install_dir/infraharvest" version)."
case ":$PATH:" in
	*":$install_dir:"*) ;;
	*) echo "Add $install_dir to your PATH to run infraharvest." ;;
esac
