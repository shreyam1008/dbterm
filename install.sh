#!/usr/bin/env bash

# dbterm installer
# Usage (Linux/macOS/Git Bash): curl -fsSL https://raw.githubusercontent.com/shreyam1008/dbterm/main/install.sh | bash

set -euo pipefail

REPO="${DBTERM_REPO:-shreyam1008/dbterm}"
BINARY_NAME="${DBTERM_BINARY_NAME:-dbterm}"
VERSION="${DBTERM_VERSION:-latest}"
INSTALL_DIR="${DBTERM_INSTALL_DIR:-}"
USE_SUDO=0
TMP_DIR=""

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

info() { echo -e "${CYAN}$*${NC}"; }
warn() { echo -e "${YELLOW}$*${NC}"; }
err() { echo -e "${RED}$*${NC}" >&2; }

cleanup() {
	if [[ -n "$TMP_DIR" && -d "$TMP_DIR" ]]; then
		rm -rf "$TMP_DIR"
	fi
}
trap cleanup EXIT

download_file() {
	local url="$1"
	local out="$2"
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL --retry 3 --retry-delay 1 "$url" -o "$out"
	elif command -v wget >/dev/null 2>&1; then
		wget -qO "$out" "$url"
	else
		err "Error: neither curl nor wget is installed."
		exit 1
	fi
}

sha256_file() {
	local file="$1"
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$file" | awk '{print $1}'
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$file" | awk '{print $1}'
	else
		return 1
	fi
}

path_contains() {
	case ":$PATH:" in
	*":$1:"*) return 0 ;;
	*) return 1 ;;
	esac
}

detect_target() {
	local os_raw arch_raw
	os_raw="$(uname -s)"
	arch_raw="$(uname -m)"

	case "$os_raw" in
	Linux) OS="linux" ;;
	Darwin) OS="darwin" ;;
	MSYS* | MINGW* | CYGWIN*) OS="windows" ;;
	*)
		err "Unsupported OS: $os_raw"
		exit 1
		;;
	esac

	case "$arch_raw" in
	x86_64 | amd64) ARCH="amd64" ;;
	aarch64 | arm64) ARCH="arm64" ;;
	*)
		err "Unsupported architecture: $arch_raw"
		exit 1
		;;
	esac
}

resolve_install_dir() {
	if [[ -n "$INSTALL_DIR" ]]; then
		return
	fi

	if [[ "$OS" == "windows" ]]; then
		INSTALL_DIR="$HOME/bin"
		return
	fi

	if [[ -w "/usr/local/bin" || "$(id -u)" -eq 0 ]]; then
		INSTALL_DIR="/usr/local/bin"
		return
	fi

	if command -v sudo >/dev/null 2>&1; then
		INSTALL_DIR="/usr/local/bin"
		USE_SUDO=1
		return
	fi

	INSTALL_DIR="$HOME/.local/bin"
}

add_path_hint() {
	local shell_name rc_file export_line
	if path_contains "$INSTALL_DIR"; then
		return
	fi

	if [[ "$OS" == "windows" ]]; then
		warn "Path not updated in this shell. Add this to your ~/.bashrc:"
		echo "export PATH=\"$INSTALL_DIR:\$PATH\""
		return
	fi

	shell_name="$(basename "${SHELL:-bash}")"
	case "$shell_name" in
	zsh) rc_file="$HOME/.zshrc" ;;
	*) rc_file="$HOME/.bashrc" ;;
	esac

	export_line="export PATH=\"$INSTALL_DIR:\$PATH\""
	if ! grep -Fqs "$export_line" "$rc_file" 2>/dev/null; then
		{
			echo ""
			echo "# dbterm"
			echo "$export_line"
		} >>"$rc_file"
		info "Added $INSTALL_DIR to PATH in $rc_file"
	fi

	warn "Open a new terminal (or run: source $rc_file) before using dbterm."
}

verify_checksum() {
	local checksum_file="$1"
	local target_file="$2"
	local asset_name="$3"
	local expected actual

	expected="$(awk -v f="$asset_name" '$2 == f { print $1 }' "$checksum_file" | head -n1)"
	if [[ -z "$expected" ]]; then
		warn "No checksum entry found for $asset_name; skipping verification."
		return
	fi

	if ! actual="$(sha256_file "$target_file")"; then
		warn "No SHA256 tool found; skipping checksum verification."
		return
	fi

	if [[ "$expected" != "$actual" ]]; then
		err "Checksum verification failed for $asset_name"
		exit 1
	fi

	info "Checksum verified."
}

main() {
	local ext asset_name target_name base_url tmp_bin tmp_checksums

	echo -e "${GREEN}Installing dbterm...${NC}"
	detect_target
	resolve_install_dir

	ext=""
	target_name="$BINARY_NAME"
	if [[ "$OS" == "windows" ]]; then
		ext=".exe"
		target_name="${BINARY_NAME}.exe"
	fi
	asset_name="${BINARY_NAME}-${OS}-${ARCH}${ext}"

	if [[ "$VERSION" == "latest" ]]; then
		base_url="https://github.com/${REPO}/releases/latest/download"
	else
		if [[ "$VERSION" != v* ]]; then
			VERSION="v${VERSION}"
		fi
		base_url="https://github.com/${REPO}/releases/download/${VERSION}"
	fi

	info "Detected target: ${OS}/${ARCH}"
	info "Install directory: ${INSTALL_DIR}"

	TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/${BINARY_NAME}-install-XXXXXX")"
	tmp_bin="${TMP_DIR}/${asset_name}"
	tmp_checksums="${TMP_DIR}/checksums.txt"

	info "Downloading ${asset_name}..."
	download_file "${base_url}/${asset_name}" "$tmp_bin"

	if download_file "${base_url}/checksums.txt" "$tmp_checksums" 2>/dev/null; then
		verify_checksum "$tmp_checksums" "$tmp_bin" "$asset_name"
	else
		warn "checksums.txt not found; continuing without checksum verification."
	fi

	chmod +x "$tmp_bin"

	if [[ "$USE_SUDO" -eq 1 ]]; then
		sudo mkdir -p "$INSTALL_DIR"
		sudo install -m 0755 "$tmp_bin" "$INSTALL_DIR/$target_name"
	else
		mkdir -p "$INSTALL_DIR"
		install -m 0755 "$tmp_bin" "$INSTALL_DIR/$target_name"
	fi

	if "$INSTALL_DIR/$target_name" --version >/dev/null 2>&1; then
		info "Binary check passed."
	else
		warn "Binary check failed, but install completed."
	fi

	add_path_hint

	echo -e "${GREEN}Success!${NC} Run ${YELLOW}dbterm${NC}."
}

main "$@"
