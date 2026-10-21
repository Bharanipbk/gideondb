//go:build !(darwin || linux || freebsd || netbsd || openbsd || dragonfly)

package diskann

import "os"

// mapFile falls back to reading the file onto the heap where the standard
// library exposes no mmap; behaviour is identical but not disk-resident.
func mapFile(path string) ([]byte, func([]byte) error, error) {
	data, err := os.ReadFile(path)
	return data, func([]byte) error { return nil }, err
}
