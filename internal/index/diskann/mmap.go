//go:build darwin || linux || freebsd || netbsd || openbsd || dragonfly

package diskann

import (
	"fmt"
	"os"
	"syscall"
)

func mapFile(path string) ([]byte, func([]byte) error, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, nil, err
	}
	if info.Size() < headerSize || uint64(info.Size()) > uint64(^uint(0)>>1) {
		return nil, nil, fmt.Errorf("invalid diskann file size")
	}
	mapped, err := syscall.Mmap(int(file.Fd()), 0, int(info.Size()), syscall.PROT_READ, syscall.MAP_SHARED)
	if err != nil {
		return nil, nil, fmt.Errorf("mmap diskann file: %w", err)
	}
	return mapped, syscall.Munmap, nil
}
