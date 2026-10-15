# syntax=docker/dockerfile:1
FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY docs ./docs
COPY gen ./gen
COPY internal ./internal
COPY pkg ./pkg
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.buildDate=${BUILD_DATE}" \
    -o /out/gideondb ./cmd/gideondb
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags="-s -w -X main.version=${VERSION}" \
    -o /out/gideondb-mcp ./cmd/gideondb-mcp
RUN mkdir -p /out/data && chown 65532:65532 /out/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --chown=65532:65532 --from=build /out/gideondb /usr/local/bin/gideondb
COPY --chown=65532:65532 --from=build /out/gideondb-mcp /usr/local/bin/gideondb-mcp
COPY --chown=65532:65532 --from=build /out/data /var/lib/gideondb
VOLUME ["/var/lib/gideondb"]
EXPOSE 6333
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD ["/usr/local/bin/gideondb", "-healthcheck-url", "https://127.0.0.1:6333/v1/health"]
ENTRYPOINT ["/usr/local/bin/gideondb"]
# HTTPS with a self-signed certificate generated into the data volume on first
# start. Production passes -tls-cert-file and -tls-key-file instead.
CMD ["-data-path", "/var/lib/gideondb", "-http-address", "0.0.0.0:6333", "-tls-self-signed"]
