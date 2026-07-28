//go:build windows

package main

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

func notifyError(err error) {
	user32 := windows.NewLazySystemDLL("user32.dll")
	messageBox := user32.NewProc("MessageBoxW")
	message, _ := windows.UTF16PtrFromString(err.Error())
	title, _ := windows.UTF16PtrFromString("EZGameLAN 管理端")
	messageBox.Call(
		0,
		uintptr(unsafe.Pointer(message)),
		uintptr(unsafe.Pointer(title)),
		0x00000010,
	)
}
