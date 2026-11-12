# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.26 AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN --mount=type=cache,target=/root/.cache/go-build CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -o /nori-gateway ./cmd/nori-gateway

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /nori-gateway /nori-gateway
EXPOSE 8080
ENTRYPOINT ["/nori-gateway"]
