package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa
#include <stdlib.h>
#include "tray_darwin.h"
*/
import "C"

import (
	"unsafe"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

const trayAvailable = true

var trayApp *App

// InstallTray hides the Dock icon and adds the menu bar item.
func InstallTray(a *App) {
	trayApp = a
	C.trayInstall()
}

// UpdateTray dims the icon when the bridge is stopped and shows running requests next to it.
func UpdateTray(running bool, active int, status string) {
	s := C.CString(status)
	defer C.free(unsafe.Pointer(s))
	r := C.int(0)
	if running {
		r = 1
	}
	C.trayUpdate(r, C.int(active), s)
}

//export trayAction
func trayAction(action C.int) {
	a := trayApp
	go func() {
		switch action {
		case 0:
			runtime.WindowShow(a.ctx)
		case 1:
			if a.State().Running {
				a.Stop()
			} else {
				a.Start()
			}
		case 2:
			runtime.Quit(a.ctx)
		}
	}()
}
