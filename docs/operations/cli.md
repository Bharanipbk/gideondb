# Operational CLI

The `gideondb` binary serves the database by default and also provides bounded
one-shot operational modes:

```text
-version                 print build version, commit and date
-validate-config         resolve and validate configuration, then exit
-verify-data             open, recover and validate the data path, then exit
-migrate-data            validate and migrate legacy checkpoints, then exit
-backup-to FILE          checkpoint, create an archive, then exit
-restore-from FILE       validate and restore into a nonexistent data path
-healthcheck-url URL     require an HTTP 200 health response within 3 seconds
-tls-ca-file PATH        private CA bundle; require verified client certificates on internal APIs
```

`-verify-data` uses the normal recovery path. It may truncate a checksummed WAL
torn tail, which is the same explicitly recoverable action performed during
server startup; it is not a byte-for-byte read-only filesystem inspection.

Build metadata is injected through `VERSION`, `COMMIT`, and `BUILD_DATE` when
running `make build` or through corresponding Docker build arguments.
-principals-file PATH        reloadable principal, role, and collection-prefix JSON file
