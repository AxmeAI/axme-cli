#!/usr/bin/env sh

set -eu

REPO="AxmeAI/axme-cli"
BIN_NAME="axme"
INSTALL_DIR="${AXME_INSTALL_DIR:-$HOME/.local/bin}"

log() {
  printf '%s\n' "$*"
}

fail() {
  printf 'axme install: %s\n' "$*" >&2
  exit 1
}

need_cmd() {
  command -v "$1" >/dev/null 2>&1
}

http_get() {
  if need_cmd curl; then
    curl -fsSL "$1"
    return
  fi
  if need_cmd wget; then
    wget -qO- "$1"
    return
  fi
  fail "curl or wget is required"
}

download_to() {
  url="$1"
  dest="$2"
  if need_cmd curl; then
    curl -fsSL "$url" -o "$dest"
    return
  fi
  if need_cmd wget; then
    wget -qO "$dest" "$url"
    return
  fi
  fail "curl or wget is required"
}

detect_os() {
  case "$(uname -s)" in
    Linux) printf 'linux' ;;
    Darwin) printf 'darwin' ;;
    *) fail "unsupported OS: $(uname -s). Download a release manually from https://github.com/${REPO}/releases" ;;
  esac
}

detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64) printf 'amd64' ;;
    arm64|aarch64) printf 'arm64' ;;
    *) fail "unsupported architecture: $(uname -m). Download a release manually from https://github.com/${REPO}/releases" ;;
  esac
}

resolve_version() {
  if [ -n "${AXME_VERSION:-}" ]; then
    case "${AXME_VERSION}" in
      v*) printf '%s' "${AXME_VERSION}" ;;
      *) printf 'v%s' "${AXME_VERSION}" ;;
    esac
    return
  fi

  json="$(http_get "https://api.github.com/repos/${REPO}/releases/latest" | tr -d '\n')"
  version="$(printf '%s' "$json" | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')"
  [ -n "$version" ] || fail "could not resolve latest release version"
  printf '%s' "$version"
}

verify_checksum() {
  asset_name="$1"
  asset_path="$2"
  checksums_path="$3"

  expected="$(awk -v asset="./${asset_name}" '$2 == asset {print $1}' "$checksums_path")"
  [ -n "$expected" ] || fail "checksum for ${asset_name} not found"

  if need_cmd sha256sum; then
    actual="$(sha256sum "$asset_path" | awk '{print $1}')"
  elif need_cmd shasum; then
    actual="$(shasum -a 256 "$asset_path" | awk '{print $1}')"
  else
    fail "sha256sum or shasum is required"
  fi

  [ "$expected" = "$actual" ] || fail "checksum verification failed for ${asset_name}"
}

OS="$(detect_os)"
ARCH="$(detect_arch)"
VERSION="$(resolve_version)"
VERSION_NO_V="${VERSION#v}"
ASSET_NAME="axme_${VERSION_NO_V}_${OS}_${ARCH}.tar.gz"
CHECKSUMS_NAME="axme_${VERSION_NO_V}_checksums.txt"
BASE_URL="https://github.com/${REPO}/releases/download/${VERSION}"

if ! need_cmd tar; then
  fail "tar is required"
fi

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT INT TERM

ASSET_PATH="${TMP_DIR}/${ASSET_NAME}"
CHECKSUMS_PATH="${TMP_DIR}/${CHECKSUMS_NAME}"

log "Downloading ${ASSET_NAME}..."
download_to "${BASE_URL}/${ASSET_NAME}" "${ASSET_PATH}"
download_to "${BASE_URL}/${CHECKSUMS_NAME}" "${CHECKSUMS_PATH}"
verify_checksum "${ASSET_NAME}" "${ASSET_PATH}" "${CHECKSUMS_PATH}"

tar -xzf "${ASSET_PATH}" -C "${TMP_DIR}"
EXTRACTED_BIN="${TMP_DIR}/axme_${VERSION_NO_V}_${OS}_${ARCH}/${BIN_NAME}"
[ -f "${EXTRACTED_BIN}" ] || fail "downloaded archive did not contain ${BIN_NAME}"

mkdir -p "${INSTALL_DIR}"
if need_cmd install; then
  install -m 0755 "${EXTRACTED_BIN}" "${INSTALL_DIR}/${BIN_NAME}"
else
  cp "${EXTRACTED_BIN}" "${INSTALL_DIR}/${BIN_NAME}"
  chmod 0755 "${INSTALL_DIR}/${BIN_NAME}"
fi

log "Installed ${BIN_NAME} ${VERSION} to ${INSTALL_DIR}/${BIN_NAME}"

case ":$PATH:" in
  *":${INSTALL_DIR}:"*) ;;
  *)
    log ""
    log "Add ${INSTALL_DIR} to your PATH if it is not already present."
    ;;
esac

log ""
log "Next steps:"
log "  axme version"
log "  axme login"
