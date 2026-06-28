//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

func enableANSIColors() {
	const enableVTP = 0x0004
	k := syscall.NewLazyDLL("kernel32.dll")
	getStd := k.NewProc("GetStdHandle")
	getMode := k.NewProc("GetConsoleMode")
	setMode := k.NewProc("SetConsoleMode")
	for _, std := range []uintptr{0xFFFFFFF5, 0xFFFFFFF4} {
		h, _, _ := getStd.Call(std)
		var mode uint32
		getMode.Call(h, uintptr(unsafe.Pointer(&mode)))
		setMode.Call(h, uintptr(mode|enableVTP))
	}
}
