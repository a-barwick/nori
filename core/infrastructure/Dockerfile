FROM --platform=$BUILDPLATFORM golang:1.26 AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -o /nori ./cmd/nori

# The official CLI supplies the supported keyless IAP TCP tunnel helper.
FROM gcr.io/google.com/cloudsdktool/google-cloud-cli:584.0.0-slim
COPY --from=build /nori /nori
RUN useradd --create-home --uid 65532 nori
USER 65532:65532
ENV BIND_HOST=0.0.0.0 PORT=8080 CLOUDSDK_CORE_DISABLE_PROMPTS=1 CLOUDSDK_CORE_DISABLE_USAGE_REPORTING=true
EXPOSE 8080
ENTRYPOINT ["/nori"]
