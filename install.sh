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
IS_TTY=0
TOTAL_STEPS=6

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
BLUE='\033[0;34m'
BOLD='\033[1m'
DIM='\033[2m'
NC='\033[0m'

info() { echo -e "${CYAN}$*${NC}"; }
warn() { echo -e "${YELLOW}$*${NC}"; }
err() { echo -e "${RED}$*${NC}" >&2; }

print_banner() {
	echo
	echo -e "${BOLD}${BLUE}+-----------------------------------------------------------+${NC}"
	echo -e "${BOLD}${BLUE}|${NC}                 ${BOLD}dbterm installer${NC}                      ${BOLD}${BLUE}|${NC}"
	echo -e "${BOLD}${BLUE}+-----------------------------------------------------------+${NC}"
	echo -e "${DIM}Standalone install for Linux, macOS, and Windows (Git Bash).${NC}"
	echo -e "${DIM}No Go toolchain required.${NC}"
}

progress_bar() {
	local idx="$1"
	local total="$2"
	local width=28
	local filled empty bar_filled bar_empty

	filled=$((idx * width / total))
	empty=$((width - filled))

	bar_filled="$(printf '%*s' "$filled" '' | tr ' ' '#')"
	bar_empty="$(printf '%*s' "$empty" '' | tr ' ' '-')"
	printf "[%s%s]" "$bar_filled" "$bar_empty"
}

print_step() {
	local idx="$1"
	local text="$2"
	local bar

	bar="$(progress_bar "$idx" "$TOTAL_STEPS")"
	echo
	echo -e "${BOLD}${CYAN}[${idx}/${TOTAL_STEPS}]${NC} ${text}"
	echo -e "    ${CYAN}${bar}${NC}"
}

run_step_plain() {
	local label="$1"
	shift

	printf "  - %s ... " "$label"
	if "$@"; then
		echo -e "${GREEN}ok${NC}"
	else
		echo -e "${RED}failed${NC}"
		return 1
	fi
}

run_step_block() {
	local label="$1"
	shift

	echo "  - ${label}"
	if "$@"; then
		echo -e "    ${GREEN}ok${NC}"
	else
		echo -e "    ${RED}failed${NC}"
		return 1
	fi
}

run_step_spinner() {
	local label="$1"
	shift

	local log_file pid rc i frame_count
	local -a frames
	log_file="$(mktemp "${TMPDIR:-/tmp}/dbterm-step-XXXXXX")"
	"$@" >"$log_file" 2>&1 &
	pid=$!

	frames=(
		"[>         ]"
		"[=>        ]"
		"[==>       ]"
		"[===>      ]"
		"[====>     ]"
		"[=====>    ]"
		"[======>   ]"
		"[=======>  ]"
		"[========> ]"
		"[=========>]"
	)
	frame_count="${#frames[@]}"

	printf "  - %s " "$label"
	i=0
	while kill -0 "$pid" 2>/dev/null; do
		printf "\r  - %s ${CYAN}%s${NC}" "$label" "${frames[$i]}"
		i=$(((i + 1) % frame_count))
		sleep 0.1
	done

	wait "$pid"
	rc=$?
	if [[ "$rc" -eq 0 ]]; then
		printf "\r  - %s ${GREEN}ok${NC}\n" "$label"
	else
		printf "\r  - %s ${RED}failed${NC}\n" "$label"
		if [[ -s "$log_file" ]]; then
			sed 's/^/    /' "$log_file" >&2
		fi
	fi
	rm -f "$log_file"
	return "$rc"
}

run_step() {
	local label="$1"
	shift
	if [[ "$IS_TTY" -eq 1 ]]; then
		run_step_spinner "$label" "$@"
	else
		run_step_plain "$label" "$@"
	fi
}

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
		return 1
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
		return 1
	fi
	return 0
}

install_binary() {
	local src_file="$1"
	local target_name="$2"

	if [[ "$USE_SUDO" -eq 1 ]]; then
		sudo mkdir -p "$INSTALL_DIR"
		sudo install -m 0755 "$src_file" "$INSTALL_DIR/$target_name"
	else
		mkdir -p "$INSTALL_DIR"
		install -m 0755 "$src_file" "$INSTALL_DIR/$target_name"
	fi
}

validate_binary() {
	local target_path="$1"
	"$target_path" --version >/dev/null 2>&1
}

prompt_for_sudo() {
	echo "  - Administrator access is required for this location."
	echo "    Password prompt is shown on the next line."
	echo
	sudo -v
	echo
}

print_quick_guide() {
	local command_hint="$1"
	local installed_version="$2"

	echo
	echo -e "${GREEN}${BOLD}Installation complete.${NC}"
	if [[ -n "$installed_version" ]]; then
		echo -e "Installed version: ${YELLOW}${installed_version}${NC}"
	fi
	echo
	echo -e "${BOLD}Quick guide${NC}"
	echo "  ${command_hint}                Launch dbterm"
	echo "  ${command_hint} --help         Show commands and key shortcuts"
	echo "  ${command_hint} --info         Show paths and system details"
	echo "  ${command_hint} --version      Show installed version"
	echo "  ${command_hint} --update       Update to latest release"
	echo "  ${command_hint} --uninstall    Uninstall binary"
	echo "  ${command_hint} --uninstall --purge --yes   Remove binary + saved connections"
}

main() {
	local ext asset_name target_name base_url tmp_bin tmp_checksums installed_version command_hint

	if [[ -t 1 ]]; then
		IS_TTY=1
	fi

	print_banner

	print_step 1 "Detecting platform"
	detect_target
	resolve_install_dir

	info "Detected target: ${OS}/${ARCH}"
	info "Install directory: ${INSTALL_DIR}"

	print_step 2 "Resolving release artifact"
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

	info "Release source: ${REPO}"
	info "Requested version: ${VERSION}"
	info "Artifact: ${asset_name}"

	TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/${BINARY_NAME}-install-XXXXXX")"
	tmp_bin="${TMP_DIR}/${asset_name}"
	tmp_checksums="${TMP_DIR}/checksums.txt"

	print_step 3 "Downloading binary"
	run_step "Downloading ${asset_name}" download_file "${base_url}/${asset_name}" "$tmp_bin"

	print_step 4 "Verifying checksum"
	if run_step "Downloading checksums.txt" download_file "${base_url}/checksums.txt" "$tmp_checksums"; then
		run_step "Verifying ${asset_name}" verify_checksum "$tmp_checksums" "$tmp_bin" "$asset_name"
		echo -e "  - Checksum ${GREEN}verified${NC}"
	else
		warn "checksums.txt not found; continuing without checksum verification."
	fi

	chmod +x "$tmp_bin"

	print_step 5 "Installing binary"
	if [[ "$USE_SUDO" -eq 1 ]]; then
		info "Installing to ${INSTALL_DIR}"
		prompt_for_sudo
	fi
	run_step_block "Installing to ${INSTALL_DIR}" install_binary "$tmp_bin" "$target_name"

	print_step 6 "Final validation"
	if run_step "Running binary check" validate_binary "$INSTALL_DIR/$target_name"; then
		installed_version="$("$INSTALL_DIR/$target_name" --version 2>/dev/null | head -n1 || true)"
		if [[ -n "$installed_version" ]]; then
			info "Installed version: ${installed_version}"
		fi
	else
		warn "Binary check failed, but install completed."
	fi

	add_path_hint

	command_hint="$BINARY_NAME"
	if ! path_contains "$INSTALL_DIR"; then
		command_hint="${INSTALL_DIR}/${target_name}"
	fi

	print_quick_guide "$command_hint" "$installed_version"
}

main "$@"
