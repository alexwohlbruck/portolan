//go:build linux && (amd64 || arm64)

package sync

import (
	"syscall"
	"unsafe"
)

const renameExchange = 1 << 1

// exchange atomically swaps two paths with renameat2(RENAME_EXCHANGE).
func exchange(a, b string) error {
	pa, err := syscall.BytePtrFromString(a)
	if err != nil {
		return err
	}
	pb, err := syscall.BytePtrFromString(b)
	if err != nil {
		return err
	}
	const atFdcwd = -100
	fd := atFdcwd
	_, _, errno := syscall.Syscall6(sysRenameat2, uintptr(fd), uintptr(unsafe.Pointer(pa)), uintptr(fd), uintptr(unsafe.Pointer(pb)), renameExchange, 0)
	if errno != 0 {
		return errno
	}
	return nil
}
