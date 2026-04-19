#!/bin/sh
# install.sh — curl-pipe-sh installer for livedocs
# Usage: curl -sSfL https://raw.githubusercontent.com/sjarmak/livedocs/main/install.sh | sh
#
# Supply-chain guarantees (parity with action.yml + examples/workflows/livedocs-prbot.yml):
#   1. AUTHENTICITY: checksums.txt is verified with cosign keyless against a
#      signing identity pinned to this repo's release.yml workflow on tag refs.
#   2. INTEGRITY:    the downloaded archive's sha256 is verified against the
#                    (now-authenticated) checksums.txt.
#
# There is no fallback to unverified install. If cosign is missing, or any
# signing artifact is absent from the release, or verification fails, the
# script aborts before the binary reaches $INSTALL_DIR.
set -eu

REPO="sjarmak/livedocs"
BINARY="livedocs"
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"

# Cosign keyless verification constants — MUST match the values used in the
# release pipeline (.goreleaser.yaml + .github/workflows/release.yml) and in
# downstream consumers (action.yml + examples/workflows/livedocs-prbot.yml).
# Any drift here silently weakens or breaks verification.
COSIGN_IDENTITY_REGEXP='^https://github\.com/sjarmak/livedocs/\.github/workflows/release\.yml@refs/tags/v.*$'
COSIGN_OIDC_ISSUER='https://token.actions.githubusercontent.com'

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

# curl wrapper: fail on HTTP errors, follow redirects, retry transient failures.
# Usage: dl URL OUTPUT_FILE   |   dl URL   (stdout)
dl() {
    if [ $# -eq 2 ]; then
        curl -sSfL --retry 3 --retry-delay 2 -o "$2" "$1"
    else
        curl -sSfL --retry 3 --retry-delay 2 "$1"
    fi
}

# Get the latest release tag from GitHub API
get_latest_version() {
    VERSION="$(dl "https://api.github.com/repos/${REPO}/releases/latest" \
        | grep '"tag_name"' \
        | sed -E 's/.*"tag_name": *"([^"]+)".*/\1/')"

    if [ -z "$VERSION" ]; then
        echo "Error: could not determine latest version" >&2
        exit 1
    fi
}

# Require cosign on PATH. We deliberately do NOT auto-bootstrap cosign:
# verifying an auto-downloaded cosign binary requires a pinned hash that
# itself needs rotation, and a bootstrap mistake here would silently weaken
# the whole chain. Asking the user to install cosign once is simpler and
# more honest than shipping a half-trusted bootstrap.
require_cosign() {
    if command -v cosign > /dev/null 2>&1; then
        return 0
    fi

    cat >&2 <<EOF
Error: cosign is required to verify the authenticity of livedocs releases
       but was not found on PATH.

Install cosign (>= v2.0) using one of:

  macOS:        brew install cosign
  Linux (apt):  sudo apt-get install -y cosign     # Debian 13+, Ubuntu 24.04+
  Go toolchain: go install github.com/sigstore/cosign/v2/cmd/cosign@latest
  Direct binary:
    https://github.com/sigstore/cosign/releases/latest

Then re-run this installer.

Why cosign is required:
  livedocs release artifacts are signed with cosign keyless (Sigstore +
  GitHub Actions OIDC). Without cosign we cannot prove that checksums.txt
  came from this repo's release workflow rather than an attacker who got
  write access to the release. This installer fails closed rather than
  silently downgrading to unverified install.

EOF
    exit 1
}

# Download, verify authenticity + integrity, and install
install() {
    detect_platform
    get_latest_version
    require_cosign

    # Strip leading 'v' for archive name
    VERSION_NUM="${VERSION#v}"

    case "$OS" in
        darwin) EXT="zip" ;;
        *)      EXT="tar.gz" ;;
    esac

    ARCHIVE="${BINARY}_${VERSION_NUM}_${OS}_${ARCH}.${EXT}"
    RELEASE_BASE="https://github.com/${REPO}/releases/download/${VERSION}"
    DOWNLOAD_URL="${RELEASE_BASE}/${ARCHIVE}"
    CHECKSUM_URL="${RELEASE_BASE}/checksums.txt"
    SIG_URL="${RELEASE_BASE}/checksums.txt.sig"
    CERT_URL="${RELEASE_BASE}/checksums.txt.pem"

    TMPDIR="$(mktemp -d)"
    trap 'rm -rf "$TMPDIR"' EXIT

    echo "Downloading ${BINARY} ${VERSION} for ${OS}/${ARCH}..."
    # Fetch the archive, the release-wide checksums.txt, and the cosign
    # signature + certificate from the same release. curl -f + --retry
    # handles transient failures; a real 404 (e.g. older release with no
    # signing artifacts) aborts under set -e before cosign runs.
    dl "$DOWNLOAD_URL" "${TMPDIR}/${ARCHIVE}"
    dl "$CHECKSUM_URL" "${TMPDIR}/checksums.txt"
    dl "$SIG_URL"      "${TMPDIR}/checksums.txt.sig"
    dl "$CERT_URL"     "${TMPDIR}/checksums.txt.pem"

    # Fail closed if any signing artifact is missing or empty. curl -f
    # already aborts on HTTP errors, but we re-assert non-empty files to
    # guard against partial writes or a zero-byte asset being published
    # in error.
    for required in "${ARCHIVE}" checksums.txt checksums.txt.sig checksums.txt.pem; do
        if [ ! -s "${TMPDIR}/${required}" ]; then
            echo "Error: release ${VERSION} is missing or empty: ${required}" >&2
            echo "       Refusing to install. Releases before v0.2 are not" >&2
            echo "       signed with cosign and cannot be installed via" >&2
            echo "       this script — use a newer release." >&2
            exit 1
        fi
    done

    # AUTHENTICITY: verify checksums.txt was signed by this repo's release
    # pipeline (not an attacker who got write access to the release and
    # swapped both the tarball and the checksums.txt).
    #
    # --certificate-identity-regexp pins the signing identity to this
    # repo's release workflow on tag refs ONLY. Without this pin, any
    # cosign keyless signature from any GitHub workflow would validate —
    # an obvious bypass. The regex is anchored start ('^') and end ('$')
    # and only accepts refs/tags/v* (not branches, not PRs, not namespaces
    # an attacker could spoof on a fork).
    #
    # --certificate-oidc-issuer pins the issuer to GitHub's official token
    # service; there is exactly one correct value for GitHub-hosted runners.
    #
    # Fulcio cert-chain verification and Rekor transparency-log inclusion
    # checks are ENABLED by default in cosign keyless mode. We intentionally
    # do NOT pass --insecure-ignore-tlog or --insecure-ignore-sct.
    #
    # If cosign verify-blob returns non-zero the step aborts (set -e)
    # BEFORE the integrity check, extraction, or install — there is no
    # fallback-to-unsigned path.
    echo "Verifying cosign signature of checksums.txt..."
    cosign verify-blob \
        --certificate "${TMPDIR}/checksums.txt.pem" \
        --signature "${TMPDIR}/checksums.txt.sig" \
        --certificate-identity-regexp "$COSIGN_IDENTITY_REGEXP" \
        --certificate-oidc-issuer "$COSIGN_OIDC_ISSUER" \
        "${TMPDIR}/checksums.txt"

    # INTEGRITY: now that checksums.txt is authenticated, verify the archive
    # bytes match the signed checksum list. Two guards:
    #   1. The archive MUST be listed in checksums.txt. sha256sum
    #      --ignore-missing silently returns 0 when the target is absent —
    #      we close that bypass with an explicit grep -qF. The two-space
    #      separator matches goreleaser's sha256sum-style output format
    #      ('<hash>  <filename>'); if that format ever changes, both this
    #      grep and the awk field-split below must be updated together.
    #   2. The sha256 must match. Under set -e any non-zero exit aborts
    #      before install, so an attacker-swapped tarball never lands.
    echo "Verifying checksum..."
    if ! grep -qF "  ${ARCHIVE}" "${TMPDIR}/checksums.txt"; then
        echo "Error: ${ARCHIVE} is not listed in checksums.txt — refusing to install." >&2
        exit 1
    fi

    if command -v sha256sum > /dev/null 2>&1; then
        ( cd "$TMPDIR" && sha256sum --ignore-missing -c checksums.txt )
    elif command -v shasum > /dev/null 2>&1; then
        # BSD shasum (macOS default) lacks --ignore-missing; filter the
        # checksum list to just our archive and verify that single entry.
        EXPECTED="$(awk -v f="${ARCHIVE}" '$2 == f {print $1}' "${TMPDIR}/checksums.txt")"
        ACTUAL="$(shasum -a 256 "${TMPDIR}/${ARCHIVE}" | awk '{print $1}')"
        if [ "$EXPECTED" != "$ACTUAL" ]; then
            echo "Error: checksum mismatch for ${ARCHIVE}" >&2
            echo "  expected: $EXPECTED" >&2
            echo "  actual:   $ACTUAL" >&2
            exit 1
        fi
    else
        echo "Error: neither sha256sum nor shasum found — cannot verify integrity." >&2
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
