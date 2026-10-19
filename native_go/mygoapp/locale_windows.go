//go:build windows

package mygoapp

import (
	"syscall"
	"unsafe"
)

func systemLocale() string {
	buf := make([]uint16, 85)
	proc := syscall.NewLazyDLL("kernel32.dll").NewProc("GetUserDefaultLocaleName")
	n, _, _ := proc.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if n == 0 {
		return "en"
	}
	return ResolveLocale(syscall.UTF16ToString(buf))
}
