#!/bin/sh
# Installs a released palate binary from GitHub Releases (assets come from
# .github/workflows/release.yml via GoReleaser: palate_<ver>_<os>_<arch>.tar.gz, no "v").
# The repo is private, so downloads need auth: an authenticated `gh`, or GITHUB_TOKEN/GH_TOKEN.
set -e
REPO="thinhkhuat/palate"
TAG="${1:-latest}"
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')
BIN_DIR="${PALATE_INSTALL_DIR:-/usr/local/bin}"
TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT

fail() { echo "$1"; echo "Fallback: go install github.com/$REPO/cmd/palate@latest (needs git access to the repo)"; exit 1; }

if command -v gh >/dev/null 2>&1 && gh auth status >/dev/null 2>&1; then
  # gh resolves "latest" itself and handles private-repo auth.
  if [ "$TAG" = "latest" ]; then
    gh release download -R "$REPO" -p "palate_*_${OS}_${ARCH}.tar.gz" -D "$TMP" || fail "gh release download failed"
  else
    gh release download "$TAG" -R "$REPO" -p "palate_${TAG#v}_${OS}_${ARCH}.tar.gz" -D "$TMP" || fail "gh release download failed"
  fi
else
  TOKEN="${GITHUB_TOKEN:-${GH_TOKEN:-}}"
  [ -n "$TOKEN" ] || fail "Private repo: run 'gh auth login' or export GITHUB_TOKEN, then re-run."
  command -v curl >/dev/null 2>&1 || fail "curl is required."
  API="https://api.github.com/repos/$REPO/releases"
  if [ "$TAG" = "latest" ]; then REL="$API/latest"; else REL="$API/tags/$TAG"; fi
  JSON=$(curl -fsSL -H "Authorization: Bearer $TOKEN" "$REL") || fail "Release lookup failed: $REL"
  TAG=$(printf '%s' "$JSON" | tr -d '\n' | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p')
  ASSET="palate_${TAG#v}_${OS}_${ARCH}.tar.gz"
  # Asset id precedes its name inside one asset object; no jq dependency.
  ID=$(printf '%s' "$JSON" | tr -d '\n' | grep -o "\"id\": *[0-9]*,[^{}]*\"name\": *\"$ASSET\"" | grep -o '[0-9]*' | head -1)
  [ -n "$ID" ] || fail "No asset $ASSET in release $TAG."
  curl -fsSL -H "Authorization: Bearer $TOKEN" -H "Accept: application/octet-stream" \
    -o "$TMP/$ASSET" "$API/assets/$ID" || fail "Asset download failed."
fi
tar -xzf "$TMP"/palate_*.tar.gz -C "$TMP"
install -m 0755 "$TMP/palate" "$BIN_DIR/palate"   # only the binary; the archive also carries README/LICENSE
"$BIN_DIR/palate" version
echo "Done. Run: palate init && palate hook install"
