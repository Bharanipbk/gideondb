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

## Current limitations

Audit retention, certificate reload, distinct client/server node identities,
and automated certificate management remain pending. Rate limits are
configurable per credential or client address.
