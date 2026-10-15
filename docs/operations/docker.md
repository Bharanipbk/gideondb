# Docker

Build the multi-stage image:

```bash
make docker IMAGE=gideondb:dev
```

The runtime image is distroless, statically linked, and runs as UID/GID 65532.
It contains the server and `gideondb-mcp` binaries, an owned `/var/lib/gideondb` directory, and
CA/runtime files supplied by the distroless base. The built-in health check uses
the binary's own HTTP health client, so no shell or curl package is required.
The build context must include `docs/` because the server embeds the Markdown
used by the `/docs/` web panel. Do not add `docs` to `.dockerignore`.

## Run it

The image starts with HTTPS on port 6333 and a self-signed certificate that it
creates in the data volume on first start (`-tls-self-signed`). A new data
volume needs dashboard credentials, and the REST API needs a key:

```bash
export GIDEONDB_API_KEY="$(openssl rand -hex 24)"
docker run -d --name gideondb -p 127.0.0.1:6333:6333 -v gideondb-data:/var/lib/gideondb \
  -e GIDEONDB_API_KEY -e GIDEONDB_DASHBOARD_USERNAME=admin \
  -e GIDEONDB_DASHBOARD_PASSWORD='choose-a-long-password' \
  ghcr.io/bharanipbk/gideondb:latest
```

Use `gideondb:dev` instead of the GHCR image for a local build. Then:

```bash
curl -k https://127.0.0.1:6333/v1/ready
curl -k -H "Authorization: Bearer $GIDEONDB_API_KEY" https://127.0.0.1:6333/v1/collections
```

`-k` skips certificate verification for the self-signed certificate. To verify
it instead, copy it out and pass `--cacert`:

```bash
docker cp gideondb:/var/lib/gideondb/tls/self-signed.crt .
curl --cacert self-signed.crt https://localhost:6333/v1/ready
```

Open the dashboard at <https://127.0.0.1:6333/dashboard/> and accept the
browser warning. The dashboard password must have at least 12 characters.

Notes:

- `-p 127.0.0.1:6333:6333` publishes the port only on the host's loopback.
  Drop `127.0.0.1:` to expose it on the network.
- `GIDEONDB_API_KEY` is visible to anyone who can run `docker inspect`. For
  shared hosts, mount a key file and pass `-api-key-file` instead.
- The built-in health check probes `https://127.0.0.1:6333/v1/health`. If you
  override the command to serve plain HTTP on loopback, also override the
  health check.

Production exposure should mount an API-key file and TLS keypair read-only and
pass `-api-key-file`, `-tls-cert-file`, `-tls-key-file`, `-tls-ca-file`, and
`-http-address 0.0.0.0:6333`. Host bind mounts must grant UID 65532 access to
the data directory. Passing `-tls-cert-file` turns off the self-signed
certificate. Kubernetes manifests are in `deployments/kubernetes`.
