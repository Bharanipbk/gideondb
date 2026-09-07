# Docker

Build the multi-stage image:

```bash
make docker IMAGE=gideondb:dev
```

The runtime image is distroless, statically linked, and runs as UID/GID 65532.
It contains only the server binary, an owned `/var/lib/gideondb` directory, and
CA/runtime files supplied by the distroless base. The built-in health check uses
the binary's own HTTP health client, so no shell or curl package is required.
The build context must include `docs/` because the server embeds the Markdown
used by the `/docs/` web panel. Do not add `docs` to `.dockerignore`.

The secure default binds to container loopback. For explicitly unauthenticated
local development exposure:

```bash
docker run --rm -p 6333:6333 -v gideondb-data:/var/lib/gideondb \
  gideondb:dev -data-path /var/lib/gideondb \
  -http-address 0.0.0.0:6333 -allow-unauthenticated
```

Verify the container after it starts:

```bash
curl -fsS http://127.0.0.1:6333/v1/health
curl -fsS http://127.0.0.1:6333/v1/ready
open http://127.0.0.1:6333/docs/
```

If the port is not reachable, confirm that `-http-address 0.0.0.0:6333` was
passed after the image name. The secure default binds to loopback inside the
container and cannot be reached through Docker's published port.

Production exposure should mount an API-key file and TLS keypair read-only and
pass `-api-key-file`, `-tls-cert-file`, `-tls-key-file`, `-tls-ca-file`, and
`-http-address 0.0.0.0:6333`. Host bind mounts must grant UID 65532 access to
the data directory. The image does not include automatic certificate issuance,
secret injection, or an orchestrator manifest.
