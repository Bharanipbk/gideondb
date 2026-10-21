package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/Bharanipbk/gideondb/internal/api/rest"
)

// liteDefaultDataPath is where -lite keeps data when no data path is
// configured: a per-user directory, so running the binary from any working
// directory finds the same collections.
func liteDefaultDataPath() string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".gideondb", "lite")
	}
	return "./gideondb-lite"
}

// checkLite rejects settings that would turn the zero-config, unauthenticated
// lite mode into a network-exposed server.
func checkLite(httpAddress, grpcAddress string, peers []string, apiKey, principalsFile string) error {
	if !rest.IsLoopbackAddress(httpAddress) {
		return fmt.Errorf("-lite listens on loopback only; %q is not a loopback address (run without -lite for a network server)", httpAddress)
	}
	if grpcAddress != "" && !rest.IsLoopbackAddress(grpcAddress) {
		return fmt.Errorf("-lite listens on loopback only; gRPC address %q is not a loopback address", grpcAddress)
	}
	if len(peers) > 0 {
		return errors.New("-lite is single-node; remove -peers")
	}
	if apiKey != "" || principalsFile != "" {
		return errors.New("-lite runs without authentication; remove the API key or principals file, or run without -lite")
	}
	return nil
}

func printLiteBanner(w io.Writer, version, httpAddress, grpcAddress, dataPath string, dashboard bool) {
	fmt.Fprintf(w, "\nGideonDB %s — lite mode (single node, loopback only, no authentication)\n", version)
	fmt.Fprintf(w, "  REST API   http://%s/v1   (try: curl http://%s/v1/health)\n", httpAddress, httpAddress)
	if grpcAddress != "" {
		fmt.Fprintf(w, "  gRPC       %s\n", grpcAddress)
	}
	if dashboard {
		fmt.Fprintf(w, "  Dashboard  http://%s/\n", httpAddress)
	} else {
		fmt.Fprintf(w, "  Dashboard  off (set GIDEONDB_DASHBOARD_USERNAME and GIDEONDB_DASHBOARD_PASSWORD to enable)\n")
	}
	fmt.Fprintf(w, "  Data       %s\n", dataPath)
	fmt.Fprintf(w, "  Not for production: see docs/operations/security.md. Press Ctrl+C to stop.\n\n")
}
