package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa
#include <stdlib.h>
#include "tray_darwin.h"
*/
import "C"

import (
	"encoding/json"
	"strings"
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

// trayGlyphs numbers the tool marks drawn in tray_darwin.m; others get a dot.
var trayGlyphs = map[string]C.int{"claude": 1, "codex": 2, "gemini": 3}

// UpdateTrayUsage draws the usage bars next to the menu bar icon and the usage panel in its menu.
func UpdateTrayUsage(m trayMeter) {
	data, _ := json.Marshal(m.Panel)
	panel := C.CString(string(data))
	defer C.free(unsafe.Pointer(panel))
	resets := C.CString(strings.Join(m.Resets, "\n"))
	defer C.free(unsafe.Pointer(resets))
	// +1 keeps the pointers valid when there are no bars.
	n := len(m.Percents)
	pcts, active, glyphs := make([]C.double, n+1), make([]C.int, n+1), make([]C.int, n+1)
	for i, p := range m.Percents {
		pcts[i] = C.double(p)
		if i < len(m.Active) {
			active[i] = C.int(m.Active[i])
		}
		if i < len(m.IDs) {
			glyphs[i] = trayGlyphs[m.IDs[i]]
		}
	}
	flags := 0
	for bit, on := range []bool{m.Vertical, m.Icon, m.Dots, m.Mono, m.Tools, len(m.Resets) > 0} {
		if on {
			flags |= 1 << bit
		}
	}
	C.trayUsage(C.int(flags), C.int(n), &pcts[0], &active[0], &glyphs[0], resets, C.int(m.Other), panel)
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
		case 3:
			a.Usage(true)
		case 4: // menu opened: fresh labels now, newer limits once the cache expires
			a.drawTrayUsage()
			a.Usage(false)
		}
	}()
}
