#!/bin/sh
# Installs a released palate binary from GitHub Releases (assets come from
# .github/workflows/release.yml via GoReleaser: palate_<ver>_<os>_<arch>.tar.gz, no "v").
# Uses an authenticated `gh` when present, else plain curl against github.com release URLs
# (no token, no API rate limit).
set -e
REPO="thinhkhuat/palate"
TAG="${1:-latest}"
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')
BIN_DIR="${PALATE_INSTALL_DIR:-/usr/local/bin}"
TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT

fail() { echo "$1"; echo "Fallback: go install github.com/$REPO/cmd/palate@latest"; exit 1; }

if command -v gh >/dev/null 2>&1 && gh auth status >/dev/null 2>&1; then
  # gh resolves "latest" itself.
  if [ "$TAG" = "latest" ]; then
    gh release download -R "$REPO" -p "palate_*_${OS}_${ARCH}.tar.gz" -D "$TMP" || fail "gh release download failed"
  else
    gh release download "$TAG" -R "$REPO" -p "palate_${TAG#v}_${OS}_${ARCH}.tar.gz" -D "$TMP" || fail "gh release download failed"
  fi
else
  command -v curl >/dev/null 2>&1 || fail "curl is required."
  if [ "$TAG" = "latest" ]; then
    # github.com/<repo>/releases/latest redirects to .../releases/tag/<tag>.
    URL=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/$REPO/releases/latest") || fail "Release lookup failed."
    TAG=${URL##*/}
    case "$TAG" in v*) ;; *) fail "No published release found for $REPO." ;; esac
  fi
  ASSET="palate_${TAG#v}_${OS}_${ARCH}.tar.gz"
  curl -fsSL -o "$TMP/$ASSET" "https://github.com/$REPO/releases/download/$TAG/$ASSET" || fail "No asset $ASSET in release $TAG."
fi
tar -xzf "$TMP"/palate_*.tar.gz -C "$TMP"
install -m 0755 "$TMP/palate" "$BIN_DIR/palate"   # only the binary; the archive also carries README/LICENSE
"$BIN_DIR/palate" version
echo "Done. Run: palate init && palate hook install"
