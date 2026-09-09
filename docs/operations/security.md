# Security foundations

The server defaults to the loopback address. It refuses an unauthenticated
non-loopback bind unless `-allow-unauthenticated` is explicitly supplied.

## Bearer authentication

Place a random token containing at least 16 characters in a file readable only
by its owner or the process's effective/supplementary group, then run:

```bash
gideondb -api-key-file /run/secrets/gideondb-api-key \
  -tls-cert-file /run/tls/tls.crt -tls-key-file /run/tls/tls.key \
  -tls-ca-file /run/tls/ca.crt \
  -http-address 0.0.0.0:6333
```

Clients send `Authorization: Bearer <token>`. Token digests are compared in
constant time. Collection, vector, search, and metrics routes require the token;
`/v1/health` and the detail-free `/v1/ready` remain unauthenticated for process
and traffic probes. API key material and
authorization headers are never logged.

For multiple independently rotatable credentials, use a mode-`0600`
`-principals-file` instead of, or alongside, the legacy key:

```json
{
  "principals": [
    {"name":"tenant-a-reader","key":"replace-with-16-plus-chars","role":"reader","collection_prefixes":["tenant-a."]},
    {"name":"tenant-a-writer","key":"replace-with-another-key","role":"writer","collection_prefixes":["tenant-a."]},
    {"name":"operator","key":"replace-with-admin-key","role":"admin"}
  ]
}
```

The file is re-read for every authenticated request, so an atomic Secret/file
replacement rotates keys without restarting. Invalid replacements fail closed.
`reader` can read and search, `writer` can additionally mutate records, and
`admin` can manage schemas and cluster operations. Non-admin principals see
and access only collection names matching one of their prefixes; using a
tenant-owned prefix such as `tenant-a.` provides collection-level tenant
isolation. Internal cluster calls require an admin credential (the legacy
`-api-key-file` remains an unrestricted cluster credential).

On Unix-like platforms the API-key file rejects all access by other users.
Group access is limited to read-only and is accepted only when the file group
matches the process's effective or supplementary groups; this supports
Kubernetes projected Secrets with `fsGroup`. Windows ACL validation is outside
the current implementation.

## Transport and headers

Authenticated non-loopback listeners require TLS unless the operator explicitly
sets `-allow-insecure-http`. Supplying only one of the TLS certificate/key flags
is rejected. Configuring `-tls-ca-file` enables private-CA mutual TLS: all
outbound discovery and internal fanout calls present the node certificate and
validate peer servers, while every inbound `/v1/internal/*` route requires a
verified client certificate. Responses include `nosniff`, frame denial, no-referrer, and
`no-store` headers.

Certificate, private-key, and CA files are loaded again for every new TLS
handshake, so atomic secret replacement takes effect without restart. Existing
connections retain their negotiated identity. The server certificate is also
the outbound node identity by default; set `-node-tls-cert-file` and
`-node-tls-key-file` to use a distinct client certificate for cluster traffic.
Keep old and new issuers in the CA bundle during an issuer rotation.

## Current limitations

### Certificate lifecycle and rotation

Use a private CA managed outside GideonDB. Issue short-lived leaf certificates,
monitor expiry externally, retain the private CA key outside the workload, and
mount `ca.crt`, `tls.crt`, and `tls.key` read-only. New TLS handshakes reload all
three files; existing connections keep their negotiated identity until they
reconnect.

For the supported Kubernetes deployment, validate and publish a replacement:

```sh
scripts/rotate-kubernetes-tls.sh \
  --ca-bundle ./ca-bundle.pem --cert ./tls.crt --key ./tls.key --dry-run
scripts/rotate-kubernetes-tls.sh \
  --ca-bundle ./ca-bundle.pem --cert ./tls.crt --key ./tls.key
```

The helper rejects unreadable files, certificates expiring within 24 hours,
untrusted leaves, and certificate/key mismatches before changing the Secret.
Kubernetes updates projected Secret volumes asynchronously, so wait for every
pod mount to change and verify a fresh TLS connection to each pod before
removing old material. Do not use `subPath` mounts because they do not receive
projected Secret updates.

For a leaf-only rotation, publish the new leaf and unchanged CA bundle, verify
each pod with a new connection, then revoke the old leaf. For an issuer change:

1. Publish a bundle containing both old and new CA certificates with the old
   leaf. Verify old-issuer traffic still succeeds.
2. Publish the same overlap bundle with the new-issuer leaf. Verify every pod
   accepts and presents the new identity; keep the old issuer available for
   rollback.
3. After all clients and peers trust the new issuer, publish the new CA alone.
   Verify readiness and peer convergence before revoking the old issuer.

Rollback by republishing the last known-good bundle, certificate, and key with
the same helper. Copy the equivalent validated files into an atomic projected
secret for non-Kubernetes containers. API bearer-key rotation is independent:
use the reloadable principals file for zero-restart rotation; legacy
`-api-key-file` changes require a controlled rolling restart.

Audit retention, rate limits, certificate reload, and separate server/client
node identities are supported.
