//go:build !windows

package rest

import (
	"os"
	"syscall"
)

func secureAPIKeyFile(info os.FileInfo) bool {
	permissions := info.Mode().Perm()
	if permissions&0o007 != 0 || permissions&0o030 != 0 {
		return false
	}
	if permissions&0o040 == 0 {
		return true
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	fileGroup := int(stat.Gid)
	if fileGroup == os.Getegid() {
		return true
	}
	groups, err := os.Getgroups()
	if err != nil {
		return false
	}
	for _, group := range groups {
		if group == fileGroup {
			return true
		}
	}
	return false
}
