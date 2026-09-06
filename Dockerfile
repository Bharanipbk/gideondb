# syntax=docker/dockerfile:1
FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY docs ./docs
COPY internal ./internal
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.buildDate=${BUILD_DATE}" \
    -o /out/gideondb ./cmd/gideondb
RUN mkdir -p /out/data && chown 65532:65532 /out/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --chown=65532:65532 --from=build /out/gideondb /usr/local/bin/gideondb
COPY --chown=65532:65532 --from=build /out/data /var/lib/gideondb
VOLUME ["/var/lib/gideondb"]
EXPOSE 6333
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD ["/usr/local/bin/gideondb", "-healthcheck-url", "http://127.0.0.1:6333/v1/health"]
ENTRYPOINT ["/usr/local/bin/gideondb"]
CMD ["-data-path", "/var/lib/gideondb"]
