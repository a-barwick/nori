# Version the resulting image when changing system tools; keep task installs in
# /workspace/tasks/<task-id> and browser/user state in the mounted agent home.
FROM debian:12.13-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
    bash ca-certificates chromium curl git jq python3 python3-websocket socat \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --create-home --uid 1000 --shell /bin/bash agent \
    && mkdir /workspace && chown agent:agent /workspace
COPY start-computer.sh /usr/local/bin/start-computer
COPY close-browser.py /usr/local/bin/close-browser.py
USER agent
ENV HOME=/home/agent
WORKDIR /workspace
EXPOSE 9222
ENTRYPOINT ["/bin/sh", "/usr/local/bin/start-computer"]
