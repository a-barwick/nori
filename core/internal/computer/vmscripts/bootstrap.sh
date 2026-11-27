#!/usr/bin/env bash
set -euo pipefail

# Idempotent bootstrap for a Nori Personal Agent Computer.
# Installs desktop (Xvfb/fluxbox/x11vnc/websockify/noVNC), headed Chromium + Playwright,
# Python document/data libraries, open-source CLI/doc tools, and Noto + Noto CJK fonts.

BOOTSTRAP_VERSION="${NORI_BOOTSTRAP_VERSION:-v1}"
MARKER="/home/nori/.nori/bootstrap-${BOOTSTRAP_VERSION}.done"

if [[ -f "${MARKER}" ]]; then
  exit 0
fi

SUDO=""
if [[ "$(id -u)" -ne 0 ]] && command -v sudo >/dev/null 2>&1; then
  SUDO="sudo"
fi

# Ensure /home/nori layout exists.
mkdir -p /home/nori/workspace /home/nori/workspace/uploads /home/nori/.config/chromium /home/nori/.local/bin /home/nori/.local/npm/bin /home/nori/.nori/bin /home/nori/.nori/logs

# Route global npm installs into /home/nori/.local/npm so they need no sudo,
# stay inside /home/nori, and take precedence over base-image CLIs in .local/bin.
touch /home/nori/.npmrc
if ! grep -Eq '^[[:space:]]*prefix[[:space:]]*=' /home/nori/.npmrc; then
  printf '\nprefix=/home/nori/.local/npm\n' >> /home/nori/.npmrc
fi

export DEBIAN_FRONTEND=noninteractive

if command -v apt-get >/dev/null 2>&1; then
  ${SUDO} apt-get update -y
  ${SUDO} apt-get install -y --no-install-recommends \
    ca-certificates curl wget gnupg git gh jq ripgrep fd-find sqlite3 make gcc g++ procps xz-utils \
    python3 python3-pip python3-venv \
    xvfb fluxbox x11vnc websockify novnc dbus-x11 xdotool \
    pandoc poppler-utils qpdf libreoffice-writer libreoffice-calc libreoffice-impress \
    tesseract-ocr ffmpeg imagemagick \
    fonts-noto-core fonts-noto-cjk fonts-noto-color-emoji fonts-liberation \
    libnss3 libatk1.0-0 libatk-bridge2.0-0 libcups2 libdrm2 libxkbcommon0 \
    libxcomposite1 libxdamage1 libxfixes3 libxrandr2 libgbm1 libasound2t64 || \
  ${SUDO} apt-get install -y --no-install-recommends \
    ca-certificates curl wget gnupg git jq ripgrep fd-find sqlite3 make gcc g++ procps xz-utils \
    python3 python3-pip python3-venv \
    xvfb fluxbox x11vnc websockify novnc dbus-x11 xdotool \
    pandoc poppler-utils qpdf libreoffice-core libreoffice-writer libreoffice-calc \
    tesseract-ocr ffmpeg imagemagick \
    fonts-noto-core fonts-noto-cjk fonts-noto-color-emoji fonts-liberation

fi

# Node.js LTS (if missing)
if ! command -v node >/dev/null 2>&1; then
  curl -fsSL https://deb.nodesource.com/setup_22.x | ${SUDO} bash -
  ${SUDO} apt-get install -y nodejs
fi

# Typst CLI (for crisp PDF typesetting with Noto/CJK support)
if ! command -v typst >/dev/null 2>&1; then
  ARCH="$(uname -m)"
  case "${ARCH}" in
    x86_64) TYPST_TARGET="x86_64-unknown-linux-musl" ;;
    aarch64|arm64) TYPST_TARGET="aarch64-unknown-linux-musl" ;;
    *) TYPST_TARGET="" ;;
  esac
  if [[ -n "${TYPST_TARGET}" ]]; then
    curl -fsSL "https://github.com/typst/typst/releases/latest/download/typst-${TYPST_TARGET}.tar.xz" -o /tmp/typst.tar.xz || true
    if [[ -f /tmp/typst.tar.xz ]]; then
      tar -xJf /tmp/typst.tar.xz -C /tmp
      find /tmp -maxdepth 2 -name typst -type f -exec mv {} /home/nori/.local/bin/typst \;
      chmod +x /home/nori/.local/bin/typst || true
      rm -rf /tmp/typst.tar.xz /tmp/typst-*
    fi
  fi
fi

# Python virtual environment for document & data tools
if [[ ! -d /home/nori/.local/venv ]]; then
  python3 -m venv /home/nori/.local/venv
fi
/home/nori/.local/venv/bin/pip install --no-cache-dir --upgrade pip >/dev/null 2>&1 || true
/home/nori/.local/venv/bin/pip install --no-cache-dir \
  pandas numpy scipy matplotlib openpyxl pdfplumber pypdf python-docx icalendar Pillow duckdb uv || true

# Playwright (for nori-browser CDP control)
mkdir -p /home/nori/.nori/browser
if [[ ! -f /home/nori/.nori/browser/node_modules/playwright-core/package.json ]]; then
  (cd /home/nori/.nori/browser && npm install --no-audit --no-fund playwright-core@1.65.1)
fi

# Ubuntu's chromium-browser package is a Snap launcher. Use Playwright's headed
# browser when the image does not already have a runnable Chromium.
has_chromium() {
  local candidate
  for candidate in chromium chromium-browser google-chrome; do
    if "${candidate}" --version >/dev/null 2>&1; then
      return 0
    fi
  done
  return 1
}
if ! has_chromium; then
  (
    cd /home/nori/.nori/browser
    export PLAYWRIGHT_BROWSERS_PATH=0
    ./node_modules/.bin/playwright-core install chromium --no-shell
    ln -sfn "$(node -p "require('playwright-core').chromium.executablePath()")" /home/nori/.local/bin/chromium
  )
fi

# Ensure PATH includes ~/.nori/bin, ~/.local/npm/bin, ~/.local/bin, and ~/.local/venv/bin for login shells.
PROFILE_SNIPPET='export NPM_CONFIG_PREFIX="/home/nori/.local/npm" PATH="/home/nori/.nori/bin:/home/nori/.local/npm/bin:/home/nori/.local/bin:/home/nori/.local/venv/bin:$PATH"'
for rc in /home/nori/.bashrc /home/nori/.profile; do
  touch "${rc}"
  if ! grep -Fq "/home/nori/.local/npm/bin" "${rc}"; then
    printf '\n%s\n' "${PROFILE_SNIPPET}" >> "${rc}"
  fi
done

if command -v fc-cache >/dev/null 2>&1; then
  fc-cache -f >/dev/null 2>&1 || true
fi

# Verify required capabilities are installed before writing the completion marker.
export NPM_CONFIG_PREFIX="/home/nori/.local/npm"
export PATH="/home/nori/.nori/bin:/home/nori/.local/npm/bin:/home/nori/.local/bin:/home/nori/.local/venv/bin:${PATH}"
for bin in Xvfb x11vnc websockify node python3 typst; do
  if ! command -v "${bin}" >/dev/null 2>&1; then
    echo "bootstrap capability check failed: missing ${bin}" >&2
    exit 1
  fi
done
if ! has_chromium; then
  echo "bootstrap capability check failed: missing chromium browser" >&2
  exit 1
fi
if ! /home/nori/.local/venv/bin/python -c "import pandas, openpyxl, pdfplumber" >/dev/null 2>&1; then
  echo "bootstrap capability check failed: python document stack missing" >&2
  exit 1
fi
if [[ ! -f /home/nori/.nori/browser/node_modules/playwright-core/package.json ]]; then
  echo "bootstrap capability check failed: playwright-core missing" >&2
  exit 1
fi

touch "${MARKER}"
