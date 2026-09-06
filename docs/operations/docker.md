# Docker

Build the multi-stage image:

```bash
make docker IMAGE=gideondb:dev
```

The runtime image is distroless, statically linked, and runs as UID/GID 65532.
It contains only the server binary, an owned `/var/lib/gideondb` directory, and
CA/runtime files supplied by the distroless base. The built-in health check uses
the binary's own HTTP health client, so no shell or curl package is required.

The secure default binds to container loopback. For explicitly unauthenticated
local development exposure:

```bash
docker run --rm -p 6333:6333 -v gideondb-data:/var/lib/gideondb \
  gideondb:dev -data-path /var/lib/gideondb \
  -http-address 0.0.0.0:6333 -allow-unauthenticated
```

Production exposure should mount an API-key file and TLS keypair read-only and
pass `-api-key-file`, `-tls-cert-file`, `-tls-key-file`, `-tls-ca-file`, and
`-http-address 0.0.0.0:6333`. Host bind mounts must grant UID 65532 access to
the data directory. The image does not include automatic certificate issuance,
secret injection, or an orchestrator manifest.
