#!/bin/sh
# diting installer
# Usage: curl -fsSL https://raw.githubusercontent.com/odradekk/diting/main/install.sh | sh
# Override install dir:   DITING_INSTALL_DIR=/usr/local/bin sh install.sh
# Pin a version:          DITING_VERSION=v2.0.1 sh install.sh
set -eu

REPO="odradekk/diting"
INSTALL_DIR="${DITING_INSTALL_DIR:-$HOME/.local/bin}"

# --- detect OS ---
case "$(uname -s)" in
  Linux)  OS="linux" ;;
  Darwin) OS="darwin" ;;
  *)
    echo "Unsupported OS: $(uname -s)" >&2
    echo "Windows users: download a binary from https://github.com/${REPO}/releases" >&2
    exit 1
    ;;
esac

# --- detect arch ---
case "$(uname -m)" in
  x86_64)          ARCH="amd64" ;;
  aarch64|arm64)   ARCH="arm64" ;;
  *)
    echo "Unsupported architecture: $(uname -m)" >&2
    echo "Supported: x86_64 (amd64), aarch64/arm64" >&2
    exit 1
    ;;
esac

# --- resolve version ---
if [ -z "${DITING_VERSION:-}" ]; then
  echo "Fetching latest release tag..."
  DITING_VERSION=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
    | grep '"tag_name"' \
    | sed 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/')
  if [ -z "$DITING_VERSION" ]; then
    echo "Failed to fetch latest release tag. Set DITING_VERSION manually." >&2
    exit 1
  fi
fi

echo "Installing diting ${DITING_VERSION} (${OS}/${ARCH})..."

# --- prepare temp dir ---
TMPDIR=$(mktemp -d)
cleanup() { rm -rf "$TMPDIR"; }
trap cleanup EXIT

ARCHIVE="diting_${DITING_VERSION}_${OS}_${ARCH}.tar.gz"
BASE_URL="https://github.com/${REPO}/releases/download/${DITING_VERSION}"

# --- download archive ---
echo "Downloading ${ARCHIVE}..."
curl -fsSL "${BASE_URL}/${ARCHIVE}" -o "${TMPDIR}/${ARCHIVE}"

# --- download and verify checksum ---
# Checksum verification is mandatory unless DITING_SKIP_CHECKSUM=1 is set.
SUMS_FILE="SHA256SUMS"
if [ "${DITING_SKIP_CHECKSUM:-0}" = "1" ]; then
  echo "Warning: checksum verification skipped (DITING_SKIP_CHECKSUM=1)." >&2
else
  if ! curl -fsSL "${BASE_URL}/${SUMS_FILE}" -o "${TMPDIR}/${SUMS_FILE}" 2>/dev/null; then
    echo "Error: could not download ${SUMS_FILE} for verification." >&2
    echo "Set DITING_SKIP_CHECKSUM=1 to bypass (not recommended)." >&2
    exit 1
  fi
  # Use awk for exact filename match to avoid regex issues with tag names.
  EXPECTED=$(awk -v f="${ARCHIVE}" '$2 == f { print $1; found=1 } END { if (!found) exit 1 }' "${TMPDIR}/${SUMS_FILE}")
  if [ $? -ne 0 ] || [ -z "$EXPECTED" ]; then
    echo "Error: ${ARCHIVE} not found in ${SUMS_FILE}." >&2
    exit 1
  fi
  if command -v sha256sum >/dev/null 2>&1; then
    echo "${EXPECTED}  ${TMPDIR}/${ARCHIVE}" | sha256sum -c -
  elif command -v shasum >/dev/null 2>&1; then
    echo "${EXPECTED}  ${TMPDIR}/${ARCHIVE}" | shasum -a 256 -c -
  else
    echo "Error: no sha256sum or shasum found; cannot verify checksum." >&2
    echo "Set DITING_SKIP_CHECKSUM=1 to bypass (not recommended)." >&2
    exit 1
  fi
fi

# --- extract ---
tar -xzf "${TMPDIR}/${ARCHIVE}" -C "${TMPDIR}"

# --- install ---
mkdir -p "$INSTALL_DIR"
cp "${TMPDIR}/diting" "${INSTALL_DIR}/diting"
chmod 755 "${INSTALL_DIR}/diting"

echo "Installed to ${INSTALL_DIR}/diting"

# --- PATH hint ---
case ":${PATH}:" in
  *":${INSTALL_DIR}:"*)
    ;;
  *)
    echo ""
    echo "Note: ${INSTALL_DIR} is not on your PATH."
    echo "Add the following line to your shell profile:"
    echo "  export PATH=\"${INSTALL_DIR}:\$PATH\""
    ;;
esac
