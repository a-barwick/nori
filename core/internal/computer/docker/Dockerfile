# Local development & VST image for Nori Personal Agent Computer.
# Mirrors the Fly.io Sprites bootstrap stack: Xvfb + fluxbox + x11vnc + websockify/noVNC +
# headed Chromium + Playwright + Python doc/data tools + Typst/Pandoc/LibreOffice + Noto/CJK fonts.
FROM debian:bookworm-slim

ENV DEBIAN_FRONTEND=noninteractive
ENV HOME=/home/nori
ENV NPM_CONFIG_PREFIX=/home/nori/.local/npm
ENV PATH="/home/nori/.nori/bin:/home/nori/.local/npm/bin:/home/nori/.local/bin:/home/nori/.local/venv/bin:${PATH}"

RUN apt-get update -y && apt-get install -y --no-install-recommends \
    ca-certificates curl wget gnupg git jq ripgrep fd-find sqlite3 make gcc g++ procps xz-utils \
    python3 python3-pip python3-venv \
    xvfb fluxbox x11vnc websockify novnc dbus-x11 xdotool \
    pandoc poppler-utils qpdf libreoffice-writer libreoffice-calc libreoffice-impress \
    tesseract-ocr ffmpeg imagemagick \
    fonts-noto-core fonts-noto-cjk fonts-noto-color-emoji fonts-liberation \
    chromium \
    && rm -rf /var/lib/apt/lists/*

# Node.js 22 LTS
RUN curl -fsSL https://deb.nodesource.com/setup_22.x | bash - \
    && apt-get install -y --no-install-recommends nodejs \
    && rm -rf /var/lib/apt/lists/*

# Create persistent non-root user & workspace layout
RUN useradd -m -d /home/nori -s /bin/bash nori \
    && mkdir -p /home/nori/workspace /home/nori/workspace/uploads /home/nori/.config/chromium /home/nori/.local/bin /home/nori/.local/npm/bin /home/nori/.nori/bin /home/nori/.nori/logs /home/nori/.nori/browser \
    && printf 'prefix=/home/nori/.local/npm\n' > /home/nori/.npmrc \
    && chown -R nori:nori /home/nori

# Typst CLI (for crisp PDF typesetting with Noto + Noto CJK glyphs)
RUN ARCH="$(uname -m)" && \
    case "${ARCH}" in \
      x86_64) TYPST_TARGET="x86_64-unknown-linux-musl" ;; \
      aarch64|arm64) TYPST_TARGET="aarch64-unknown-linux-musl" ;; \
      *) TYPST_TARGET="" ;; \
    esac && \
    if [ -n "${TYPST_TARGET}" ]; then \
      curl -fsSL "https://github.com/typst/typst/releases/latest/download/typst-${TYPST_TARGET}.tar.xz" -o /tmp/typst.tar.xz && \
      tar -xJf /tmp/typst.tar.xz -C /tmp && \
      find /tmp -maxdepth 2 -name typst -type f -exec mv {} /home/nori/.local/bin/typst \; && \
      chmod +x /home/nori/.local/bin/typst && \
      chown nori:nori /home/nori/.local/bin/typst && \
      rm -rf /tmp/typst.tar.xz /tmp/typst-*; \
    fi

# Python document & data stack
RUN python3 -m venv /home/nori/.local/venv \
    && /home/nori/.local/venv/bin/pip install --no-cache-dir --upgrade pip \
    && /home/nori/.local/venv/bin/pip install --no-cache-dir \
       pandas numpy scipy matplotlib openpyxl pdfplumber pypdf python-docx icalendar Pillow duckdb uv \
    && chown -R nori:nori /home/nori/.local

# Playwright core for nori-browser CDP helper
RUN cd /home/nori/.nori/browser \
    && npm init -y \
    && npm install --no-audit --no-fund playwright-core \
    && chown -R nori:nori /home/nori/.nori

RUN fc-cache -f

USER nori
WORKDIR /home/nori/workspace
EXPOSE 8080
CMD ["sleep", "infinity"]
