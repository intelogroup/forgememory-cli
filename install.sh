#!/bin/sh
# install.sh — download and install forge from GitHub releases
# Usage: curl -fsSL https://raw.githubusercontent.com/intelogroup/forgememory-cli/main/install.sh | sh
set -e

REPO="intelogroup/forgememory-cli"
INSTALL_DIR="${FORGE_INSTALL_DIR:-/usr/local/bin}"

# Detect OS and arch
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)

case "$ARCH" in
  x86_64|amd64) ARCH="amd64" ;;
  arm64|aarch64) ARCH="arm64" ;;
  *) echo "Unsupported architecture: $ARCH" >&2; exit 1 ;;
esac

# Handle Windows
case "$OS" in
  msys*|cygwin*|mingw*) OS="windows" ;;
esac

# Resolve latest tag if not specified
VERSION="${FORGE_VERSION:-}"
if [ -z "$VERSION" ]; then
  VERSION=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" \
    | grep '"tag_name"' | sed 's/.*"tag_name": *"\(.*\)".*/\1/')
fi

if [ -z "$VERSION" ]; then
  echo "Could not determine latest version. Set FORGE_VERSION=vX.Y.Z to override." >&2
  exit 1
fi

BASE_URL="https://github.com/$REPO/releases/download/$VERSION"
TMP=$(mktemp -d)

echo "Installing forge $VERSION ($OS/$ARCH) → $INSTALL_DIR"

if [ "$OS" = "windows" ]; then
  ARCHIVE="forge-windows-${ARCH}.zip"
  curl -fsSL "$BASE_URL/$ARCHIVE" -o "$TMP/$ARCHIVE"
  unzip -o "$TMP/$ARCHIVE" -d "$TMP"
  BINARY="forge-windows-${ARCH}.exe"
  
  if [ -w "$INSTALL_DIR" ]; then
    mv "$TMP/$BINARY" "$INSTALL_DIR/forge.exe"
  else
    echo "Needs sudo to write to $INSTALL_DIR"
    sudo mv "$TMP/$BINARY" "$INSTALL_DIR/forge.exe"
  fi
else
  ARCHIVE="forge-${OS}-${ARCH}.tar.gz"
  curl -fsSL "$BASE_URL/$ARCHIVE"     -o "$TMP/$ARCHIVE"
  curl -fsSL "$BASE_URL/SHA256SUMS"   -o "$TMP/SHA256SUMS"
  
  cd "$TMP"
  grep "$ARCHIVE" SHA256SUMS | sha256sum -c -
  
  tar xzf "$ARCHIVE"
  BINARY="forge-${OS}-${ARCH}"
  
  if [ -w "$INSTALL_DIR" ]; then
    mv "$BINARY" "$INSTALL_DIR/forge"
  else
    echo "Needs sudo to write to $INSTALL_DIR"
    sudo mv "$BINARY" "$INSTALL_DIR/forge"
  fi
fi

chmod +x "$INSTALL_DIR/forge" 2>/dev/null || true
rm -rf "$TMP"

echo ""
echo "Done!"
echo ""
if [ "$OS" = "windows" ]; then
  echo "Run: forge.exe --help"
else
  echo "Run: forge --help"
fi
