FROM golang:1.26-alpine AS build
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
RUN CGO_ENABLED=0 go build -trimpath -o /nori ./cmd/nori

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /nori /nori
USER 65532:65532
ENV PORT=8080
EXPOSE 8080
ENTRYPOINT ["/nori"]
