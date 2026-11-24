FROM --platform=$BUILDPLATFORM golang:1.26@sha256:4165ee6bf3b7811fd7b40373290405c83d0167dcb87ee5f29c427cdcef45ff8f AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN --mount=type=cache,target=/root/.cache/go-build CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -o /nori ./cmd/nori && \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -o /nori-user ./cmd/nori-user

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
ARG SENTRY_RELEASE
COPY --from=build /nori /nori
COPY --from=build /nori-user /nori-user
ENV BIND_HOST=0.0.0.0 PORT=8080 SENTRY_RELEASE=${SENTRY_RELEASE}
EXPOSE 8080
ENTRYPOINT ["/nori"]
