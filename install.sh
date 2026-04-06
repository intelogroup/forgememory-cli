#!/bin/sh
# install.sh — download and install forge from GitHub releases
# Usage: curl -fsSL https://raw.githubusercontent.com/inteligroup/forgememory-cli/main/install.sh | sh
# Or with custom prefix: curl -fsSL ... | sh -s -- --prefix ~/.local
set -e

REPO="intelogroup/forgememory-cli"

# Parse arguments
FORCE_PREFIX=""
while [ $# -gt 0 ]; do
  case "$1" in
    --prefix)
      FORCE_PREFIX="$2"
      shift 2
      ;;
    --prefix=*)
      FORCE_PREFIX="${1#--prefix=}"
      shift
      ;;
    *)
      shift
      ;;
  esac
done

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

# Determine install directory (try user-writable locations first)
if [ -n "$FORCE_PREFIX" ]; then
  INSTALL_DIR="$FORCE_PREFIX/bin"
elif [ -d "$HOME/.local/bin" ] && [ -w "$HOME/.local/bin" ]; then
  INSTALL_DIR="$HOME/.local/bin"
elif [ -d "$HOME/bin" ] && [ -w "$HOME/bin" ]; then
  INSTALL_DIR="$HOME/bin"
elif [ -d "$(brew --prefix 2>/dev/null)/bin" ] && [ -w "$(brew --prefix)/bin" ]; then
  INSTALL_DIR="$(brew --prefix)/bin"
elif [ -w "/usr/local/bin" ]; then
  INSTALL_DIR="/usr/local/bin"
else
  # Check if we can use sudo
  if sudo -n true 2>/dev/null; then
    INSTALL_DIR="/usr/local/bin"
  else
    # Try to create ~/.local/bin
    mkdir -p "$HOME/.local/bin"
    INSTALL_DIR="$HOME/.local/bin"
  fi
fi

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

# Create install dir if needed
mkdir -p "$INSTALL_DIR"

if [ "$OS" = "windows" ]; then
  ARCHIVE="forge-windows-${ARCH}.zip"
  curl -fsSL "$BASE_URL/$ARCHIVE" -o "$TMP/$ARCHIVE"
  unzip -o "$TMP/$ARCHIVE" -d "$TMP"
  BINARY="forge-windows-${ARCH}.exe"
  
  if [ -w "$INSTALL_DIR" ]; then
    mv "$TMP/$BINARY" "$INSTALL_DIR/forgememo.exe"
    cp "$INSTALL_DIR/forgememo.exe" "$INSTALL_DIR/forge.exe"
  else
    sudo mv "$TMP/$BINARY" "$INSTALL_DIR/forgememo.exe"
    sudo cp "$INSTALL_DIR/forgememo.exe" "$INSTALL_DIR/forge.exe"
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
    mv "$BINARY" "$INSTALL_DIR/forgememo"
    ln -sf "$INSTALL_DIR/forgememo" "$INSTALL_DIR/forge"
  else
    sudo mv "$BINARY" "$INSTALL_DIR/forgememo"
    sudo ln -sf "$INSTALL_DIR/forgememo" "$INSTALL_DIR/forge"
  fi
fi

chmod +x "$INSTALL_DIR/forgememo" 2>/dev/null || true
chmod +x "$INSTALL_DIR/forge" 2>/dev/null || true
rm -rf "$TMP"

echo ""
echo "Done!"
echo ""
if [ "$OS" = "windows" ]; then
  echo "Run: forgememo.exe --help"
else
  echo "Run: forgememo --help"
fi
echo ""
echo "To add to your PATH, add this to your shell config:"
echo "  export PATH=\"\$HOME/.local/bin:\$PATH\""
