#!/usr/bin/env bash
set -euo pipefail

REPO="ashwath-ramesh/autopr"
BINARY="ap"

# --- Detect OS/Arch ---
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)

case "$OS" in
  darwin) ;;
  *) echo "Error: only macOS is supported right now." >&2; exit 1 ;;
esac

case "$ARCH" in
  x86_64)  ARCH="amd64" ;;
  arm64)   ARCH="arm64" ;;
  aarch64) ARCH="arm64" ;;
  *) echo "Error: unsupported architecture $ARCH" >&2; exit 1 ;;
esac

# --- Get latest version ---
echo "Fetching latest release..."
TAG=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" | grep '"tag_name"' | sed -E 's/.*"v([^"]+)".*/\1/')

if [ -z "$TAG" ]; then
  echo "Error: could not determine latest release." >&2
  exit 1
fi

echo "Installing ap v${TAG} (${OS}/${ARCH})..."

# --- Download and extract ---
TARBALL="ap_${TAG}_${OS}_${ARCH}.tar.gz"
URL="https://github.com/${REPO}/releases/download/v${TAG}/${TARBALL}"
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

curl -fsSL "$URL" -o "${TMP}/${TARBALL}"
tar -xzf "${TMP}/${TARBALL}" -C "$TMP"

# --- Install binary ---
if [ -w /usr/local/bin ]; then
  INSTALL_DIR="/usr/local/bin"
else
  INSTALL_DIR="${HOME}/.local/bin"
  mkdir -p "$INSTALL_DIR"
fi

mv "${TMP}/${BINARY}" "${INSTALL_DIR}/${BINARY}"
chmod +x "${INSTALL_DIR}/${BINARY}"

# macOS: remove quarantine attribute
xattr -d com.apple.quarantine "${INSTALL_DIR}/${BINARY}" 2>/dev/null || true

echo ""
echo "Installed: ${INSTALL_DIR}/${BINARY} (v${TAG})"

# --- Check PATH ---
if ! echo "$PATH" | tr ':' '\n' | grep -qx "$INSTALL_DIR"; then
  echo ""
  echo "Add to your PATH:"
  echo "  export PATH=\"${INSTALL_DIR}:\$PATH\""
fi

echo ""
echo "Get started:"
echo "  ap init"
