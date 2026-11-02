# The computer's packages from infrastructure/microvm/build-guest.sh, for unpaid journeys on Linux.
# Build from core/: docker build -f e2e/guest/Dockerfile -t nori-e2e-guest .
FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY cmd ./cmd
COPY internal ./internal
RUN go build -o /computer-agent ./cmd/computer-agent

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
    chromium fonts-dejavu-core curl ca-certificates python3 python3-websocket poppler-utils iproute2 procps \
  && ln -s /usr/bin/python3 /usr/local/bin/python
COPY --from=build /computer-agent /usr/local/bin/computer-agent
COPY e2e/guest/start.sh /start.sh
ENTRYPOINT ["/bin/sh", "/start.sh"]
