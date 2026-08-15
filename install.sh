#!/bin/sh
set -e
REPO="thinhkhuat/palate"
VERSION="${1:-latest}"
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/;s/arm64/arm64/')
echo "Installing palate $VERSION for $OS/$ARCH from $REPO..."
if command -v curl >/dev/null 2>&1; then
  curl -fsSL "https://github.com/$REPO/releases/download/$VERSION/palate_${VERSION}_${OS}_${ARCH}.tar.gz" | tar -xz -C /usr/local/bin 2>&1 || echo "Download failed — build from source: go install github.com/$REPO/cmd/palate@latest"
else
  echo "Install curl or build from source: go install github.com/$REPO/cmd/palate@latest"
fi
palate doctor 2>&1 || true
echo "Done. Run: palate init && palate hook install"
