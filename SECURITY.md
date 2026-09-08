# Security policy

## Supported versions

GideonDB is pre-alpha and has not published a stable release. Security fixes
are currently made on the default branch only. Do not expose a development
build to untrusted networks without authentication, TLS, network isolation,
backups, and workload-specific validation.

## Reporting a vulnerability

Do not open a public issue or pull request for a suspected vulnerability. Use
GitHub's private vulnerability reporting feature for this repository. Include:

- affected version or commit;
- deployment and configuration assumptions;
- reproduction steps or a minimal proof of concept;
- expected and observed impact; and
- any suggested mitigation, if known.

Avoid accessing data that is not yours, disrupting shared systems, persistence,
or availability, and publishing details before a coordinated disclosure.

The maintainers aim to acknowledge a complete report within seven days, provide
an initial assessment within fourteen days, and coordinate remediation and
disclosure based on severity and release readiness. These are targets, not a
service-level agreement. Credit is offered when desired and appropriate.

## Scope

Reports concerning authentication or tenant isolation, TLS, parser or archive
safety, memory corruption, denial of service, WAL/checkpoint integrity,
replication fencing, consensus, backup/restore, and dependency vulnerabilities
are in scope. General support questions, missing hardening already documented as
pre-alpha limitations, and findings requiring prior compromise of the host are
normally handled as regular issues.
