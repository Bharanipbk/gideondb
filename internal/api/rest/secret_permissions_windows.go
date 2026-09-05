//go:build windows

package rest

import "os"

// Windows ACL validation is outside the current implementation.
func secureAPIKeyFile(os.FileInfo) bool { return true }
