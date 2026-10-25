package main

import (
	"syscall"
	"unsafe"
)

var (
	user32              = syscall.NewLazyDLL("user32.dll")
	getForegroundWindow = user32.NewProc("GetForegroundWindow")
	getWindowTextW      = user32.NewProc("GetWindowTextW")
)

// foregroundTitle returns the title of the window that has focus.
func foregroundTitle() string {
	hwnd, _, _ := getForegroundWindow.Call()
	if hwnd == 0 {
		return ""
	}
	buf := make([]uint16, 512)
	n, _, _ := getWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return syscall.UTF16ToString(buf[:n])
}
