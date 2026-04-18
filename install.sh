#!/bin/sh
# install.sh — curl-pipe-sh installer for livedocs
# Usage: curl -sSfL https://raw.githubusercontent.com/sjarmak/livedocs/main/install.sh | sh
set -e

REPO="sjarmak/livedocs"
BINARY="livedocs"
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"

# Detect OS and architecture
detect_platform() {
    OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
    ARCH="$(uname -m)"

    case "$OS" in
        linux)  OS="linux" ;;
        darwin) OS="darwin" ;;
        *)
            echo "Error: unsupported OS: $OS" >&2
            exit 1
            ;;
    esac

    case "$ARCH" in
        x86_64|amd64)   ARCH="amd64" ;;
        aarch64|arm64)   ARCH="arm64" ;;
        *)
            echo "Error: unsupported architecture: $ARCH" >&2
            exit 1
            ;;
    esac
}

# Get the latest release tag from GitHub API
get_latest_version() {
    VERSION="$(curl -sSfL "https://api.github.com/repos/${REPO}/releases/latest" \
        | grep '"tag_name"' \
        | sed -E 's/.*"tag_name": *"([^"]+)".*/\1/')"

    if [ -z "$VERSION" ]; then
        echo "Error: could not determine latest version" >&2
        exit 1
    fi
}

# Download, verify checksum, and install
install() {
    detect_platform
    get_latest_version

    # Strip leading 'v' for archive name
    VERSION_NUM="${VERSION#v}"

    case "$OS" in
        darwin) EXT="zip" ;;
        *)      EXT="tar.gz" ;;
    esac

    ARCHIVE="${BINARY}_${VERSION_NUM}_${OS}_${ARCH}.${EXT}"
    DOWNLOAD_URL="https://github.com/${REPO}/releases/download/${VERSION}/${ARCHIVE}"
    CHECKSUM_URL="https://github.com/${REPO}/releases/download/${VERSION}/checksums.txt"

    TMPDIR="$(mktemp -d)"
    trap 'rm -rf "$TMPDIR"' EXIT

    echo "Downloading ${BINARY} ${VERSION} for ${OS}/${ARCH}..."
    curl -sSfL -o "${TMPDIR}/${ARCHIVE}" "$DOWNLOAD_URL"
    curl -sSfL -o "${TMPDIR}/checksums.txt" "$CHECKSUM_URL"

    # Verify checksum
    echo "Verifying checksum..."
    EXPECTED="$(grep "${ARCHIVE}" "${TMPDIR}/checksums.txt" | awk '{print $1}')"
    if [ -z "$EXPECTED" ]; then
        echo "Error: checksum not found for ${ARCHIVE}" >&2
        exit 1
    fi

    if command -v sha256sum > /dev/null 2>&1; then
        ACTUAL="$(sha256sum "${TMPDIR}/${ARCHIVE}" | awk '{print $1}')"
    elif command -v shasum > /dev/null 2>&1; then
        ACTUAL="$(shasum -a 256 "${TMPDIR}/${ARCHIVE}" | awk '{print $1}')"
    else
        echo "Warning: no sha256 tool found, skipping checksum verification" >&2
        ACTUAL="$EXPECTED"
    fi

    if [ "$EXPECTED" != "$ACTUAL" ]; then
        echo "Error: checksum mismatch" >&2
        echo "  expected: $EXPECTED" >&2
        echo "  actual:   $ACTUAL" >&2
        exit 1
    fi

    # Extract
    echo "Extracting..."
    case "$EXT" in
        tar.gz) tar -xzf "${TMPDIR}/${ARCHIVE}" -C "$TMPDIR" ;;
        zip)    unzip -q "${TMPDIR}/${ARCHIVE}" -d "$TMPDIR" ;;
    esac

    # Install
    if [ -w "$INSTALL_DIR" ]; then
        cp "${TMPDIR}/${BINARY}" "${INSTALL_DIR}/${BINARY}"
    else
        echo "Installing to ${INSTALL_DIR} (requires sudo)..."
        sudo cp "${TMPDIR}/${BINARY}" "${INSTALL_DIR}/${BINARY}"
    fi
    chmod +x "${INSTALL_DIR}/${BINARY}"

    echo "Successfully installed ${BINARY} ${VERSION} to ${INSTALL_DIR}/${BINARY}"
}

install
