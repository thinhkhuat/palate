#!/bin/sh
# Installs a released palate binary from GitHub Releases (assets are produced by
# .github/workflows/release.yml via GoReleaser: palate_<ver>_<os>_<arch>.tar.gz, no "v").
set -e
REPO="thinhkhuat/palate"
TAG="${1:-latest}"
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')
BIN_DIR="${PALATE_INSTALL_DIR:-/usr/local/bin}"
if ! command -v curl >/dev/null 2>&1; then
  echo "Install curl or build from source: go install github.com/$REPO/cmd/palate@latest"; exit 1
fi
# "latest" → resolve the tag through GitHub's redirect (…/releases/latest → …/releases/tag/vX.Y.Z).
if [ "$TAG" = "latest" ]; then
  TAG=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/$REPO/releases/latest" | sed 's#.*/tag/##')
fi
VER="${TAG#v}"
URL="https://github.com/$REPO/releases/download/$TAG/palate_${VER}_${OS}_${ARCH}.tar.gz"
echo "Installing palate $TAG for $OS/$ARCH from $URL"
TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
if ! curl -fsSL "$URL" | tar -xz -C "$TMP"; then
  echo "Download failed — build from source: go install github.com/$REPO/cmd/palate@latest"; exit 1
fi
install -m 0755 "$TMP/palate" "$BIN_DIR/palate"   # only the binary; the archive also carries README/LICENSE
"$BIN_DIR/palate" version
echo "Done. Run: palate init && palate hook install"
