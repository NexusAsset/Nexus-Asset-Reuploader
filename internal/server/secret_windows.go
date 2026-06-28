//go:build windows

package server

import (
	"syscall"
	"unsafe"
)

var (
	crypt32       = syscall.NewLazyDLL("crypt32.dll")
	kernel32dll   = syscall.NewLazyDLL("kernel32.dll")
	procProtect   = crypt32.NewProc("CryptProtectData")
	procUnprotect = crypt32.NewProc("CryptUnprotectData")
	procLocalFree = kernel32dll.NewProc("LocalFree")
)

type dataBlob struct {
	cbData uint32
	pbData *byte
}

func newBlob(d []byte) dataBlob {
	if len(d) == 0 {
		return dataBlob{}
	}
	return dataBlob{cbData: uint32(len(d)), pbData: &d[0]}
}

func (b dataBlob) bytes() []byte {
	out := make([]byte, b.cbData)
	copy(out, unsafe.Slice(b.pbData, b.cbData))
	return out
}

// secretSeal encrypts with DPAPI (current user scope). On failure it returns
// the input unchanged so a save never silently loses data.
func secretSeal(in []byte) []byte {
	inBlob := newBlob(in)
	var out dataBlob
	r, _, _ := procProtect.Call(uintptr(unsafe.Pointer(&inBlob)), 0, 0, 0, 0, 0, uintptr(unsafe.Pointer(&out)))
	if r == 0 || out.pbData == nil {
		return in
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(out.pbData)))
	return out.bytes()
}

// secretOpen decrypts DPAPI data; ok=false if the bytes aren't valid DPAPI blobs.
func secretOpen(in []byte) ([]byte, bool) {
	inBlob := newBlob(in)
	var out dataBlob
	r, _, _ := procUnprotect.Call(uintptr(unsafe.Pointer(&inBlob)), 0, 0, 0, 0, 0, uintptr(unsafe.Pointer(&out)))
	if r == 0 || out.pbData == nil {
		return nil, false
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(out.pbData)))
	return out.bytes(), true
}
