FROM golang:1.26-alpine AS build
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
RUN CGO_ENABLED=0 go build -trimpath -o /bot-core ./cmd/bot-core

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /bot-core /bot-core
USER 65532:65532
ENV PORT=8080
EXPOSE 8080
ENTRYPOINT ["/bot-core"]
