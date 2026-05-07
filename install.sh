#!/usr/bin/env sh
# AgentOven CLI installer
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/agentoven/agentoven/main/install.sh | sh
#
# Options (environment variables):
#   AGENTOVEN_VERSION   Pin to a specific release (default: latest)
#   AGENTOVEN_BIN_DIR   Install directory (default: /usr/local/bin)
#   AGENTOVEN_NO_BREW   Set to "1" to skip Homebrew on macOS
#
# Supported platforms:
#   macOS (arm64, x86_64) · Linux (arm64, x86_64, armv7)
#
# SPDX-License-Identifier: Apache-2.0

set -eu

REPO="agentoven/agentoven"
BINARY_NAME="agentoven"
BIN_DIR="${AGENTOVEN_BIN_DIR:-/usr/local/bin}"

# ── Helpers ────────────────────────────────────────────────────────────────────

say()     { printf "  %s\n" "$*"; }
info()    { printf "\033[1;34m[agentoven]\033[0m %s\n" "$*"; }
success() { printf "\033[1;32m[agentoven]\033[0m %s\n" "$*"; }
warn()    { printf "\033[1;33m[agentoven]\033[0m WARNING: %s\n" "$*" >&2; }
err()     { printf "\033[1;31m[agentoven]\033[0m ERROR: %s\n" "$*" >&2; exit 1; }

need_cmd() {
    if ! command -v "$1" >/dev/null 2>&1; then
        err "Required command not found: $1"
    fi
}

# ── Detect OS / arch ───────────────────────────────────────────────────────────

detect_platform() {
    local os arch

    os="$(uname -s)"
    arch="$(uname -m)"

    case "$os" in
        Darwin)
            OS="darwin"
            ;;
        Linux)
            OS="linux"
            ;;
        *)
            err "Unsupported operating system: $os (supported: macOS, Linux)"
            ;;
    esac

    case "$arch" in
        x86_64 | amd64)
            ARCH="x86_64"
            ;;
        aarch64 | arm64)
            ARCH="aarch64"
            ;;
        armv7l)
            ARCH="armv7"
            ;;
        *)
            err "Unsupported architecture: $arch (supported: x86_64, aarch64, armv7)"
            ;;
    esac
}

# ── Resolve latest version ─────────────────────────────────────────────────────

resolve_version() {
    if [ -n "${AGENTOVEN_VERSION:-}" ]; then
        VERSION="$AGENTOVEN_VERSION"
        info "Using pinned version: $VERSION"
        return
    fi

    need_cmd curl

    info "Fetching latest release..."
    VERSION="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
        | grep '"tag_name"' \
        | sed -E 's/.*"tag_name": *"([^"]+)".*/\1/')"

    if [ -z "$VERSION" ]; then
        err "Could not determine latest version. Set AGENTOVEN_VERSION to install a specific version."
    fi

    info "Latest version: $VERSION"
}

# ── Homebrew fast-path (macOS) ─────────────────────────────────────────────────

try_homebrew() {
    if [ "$OS" != "darwin" ]; then
        return 1
    fi
    if [ "${AGENTOVEN_NO_BREW:-0}" = "1" ]; then
        return 1
    fi
    if ! command -v brew >/dev/null 2>&1; then
        return 1
    fi

    info "Homebrew detected — installing via tap..."
    brew install agentoven/tap/agentoven && return 0 || return 1
}

# ── Binary download ────────────────────────────────────────────────────────────

download_binary() {
    need_cmd curl

    local tag="$VERSION"
    local archive_name="${BINARY_NAME}-${OS}-${ARCH}.tar.gz"
    local url="https://github.com/${REPO}/releases/download/${tag}/${archive_name}"
    local tmp_dir
    tmp_dir="$(mktemp -d)"

    info "Downloading $archive_name..."
    if ! curl -fsSL --progress-bar "$url" -o "${tmp_dir}/${archive_name}"; then
        err "Download failed: $url"
    fi

    # Verify checksum if available
    local checksum_url="${url}.sha256"
    if curl -fsSL "$checksum_url" -o "${tmp_dir}/${archive_name}.sha256" 2>/dev/null; then
        info "Verifying checksum..."
        (cd "$tmp_dir" && sha256sum --check "${archive_name}.sha256" >/dev/null 2>&1) \
            || (cd "$tmp_dir" && shasum -a 256 --check "${archive_name}.sha256" >/dev/null 2>&1) \
            || warn "Checksum verification failed — proceeding anyway (upgrade curl or set AGENTOVEN_VERSION)"
    fi

    info "Extracting..."
    tar -xzf "${tmp_dir}/${archive_name}" -C "$tmp_dir"

    install_binary "$tmp_dir"
    rm -rf "$tmp_dir"
}

install_binary() {
    local src_dir="$1"
    local src="${src_dir}/${BINARY_NAME}"

    if [ ! -f "$src" ]; then
        # Some archives nest the binary one level deeper
        src="${src_dir}/${BINARY_NAME}-${OS}-${ARCH}/${BINARY_NAME}"
    fi
    if [ ! -f "$src" ]; then
        err "Binary not found in archive. Archive contents: $(ls "$src_dir")"
    fi

    if [ ! -d "$BIN_DIR" ]; then
        mkdir -p "$BIN_DIR" 2>/dev/null || {
            warn "Cannot create $BIN_DIR — trying with sudo"
            sudo mkdir -p "$BIN_DIR"
        }
    fi

    if [ -w "$BIN_DIR" ]; then
        mv "$src" "${BIN_DIR}/${BINARY_NAME}"
        chmod +x "${BIN_DIR}/${BINARY_NAME}"
    else
        info "Installing to $BIN_DIR (requires sudo)..."
        sudo mv "$src" "${BIN_DIR}/${BINARY_NAME}"
        sudo chmod +x "${BIN_DIR}/${BINARY_NAME}"
    fi
}

# ── Post-install ───────────────────────────────────────────────────────────────

print_success() {
    success "agentoven $VERSION installed to ${BIN_DIR}/${BINARY_NAME}"
    echo ""
    say "Next steps:"
    say ""
    say "  1. Start the control plane (Docker):"
    say "       curl -fsSL https://raw.githubusercontent.com/${REPO}/main/docker-compose.yml | docker compose -f - up -d"
    say ""
    say "  2. Add a model provider:"
    say "       agentoven provider add my-openai --kind openai --api-key \$OPENAI_API_KEY"
    say ""
    say "  3. Bake your first agent:"
    say "       agentoven agent register my-agent --model-provider my-openai --model-name gpt-4o"
    say "       agentoven agent bake my-agent"
    say ""
    say "  4. Explore examples:"
    say "       https://github.com/${REPO}/tree/main/examples"
    say ""
    say "  Documentation: https://docs.agentoven.dev"
    say "  Discord:       https://discord.gg/WxTn6rtpzT"
    echo ""
}

check_existing() {
    if command -v "$BINARY_NAME" >/dev/null 2>&1; then
        local existing
        existing="$($BINARY_NAME --version 2>/dev/null || echo "unknown")"
        info "Existing installation found: $existing"
    fi
}

# ── Main ───────────────────────────────────────────────────────────────────────

main() {
    echo ""
    info "AgentOven CLI Installer"
    echo ""

    detect_platform
    info "Platform: ${OS}/${ARCH}"

    check_existing
    resolve_version

    if try_homebrew; then
        print_success
        return 0
    fi

    download_binary
    print_success
}

main "$@"
