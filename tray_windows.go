package main

import (
	_ "embed"
	goruntime "runtime"
	"strconv"
	"sync"

	"github.com/energye/systray"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

const trayAvailable = true

//go:embed build/windows/icon.ico
var trayIcon []byte

var tray struct {
	mu             sync.Mutex
	status, toggle *systray.MenuItem
	running        bool
	active         int
	text           string
}

// InstallTray adds the notification area icon; its message loop needs a dedicated OS thread.
func InstallTray(a *App) {
	go func() {
		goruntime.LockOSThread()
		systray.Run(func() {
			systray.SetIcon(trayIcon)
			systray.SetOnClick(func(systray.IMenu) { runtime.WindowShow(a.ctx) })
			tray.mu.Lock()
			tray.status = systray.AddMenuItem("Starting…", "")
			tray.status.Disable()
			systray.AddSeparator()
			systray.AddMenuItem("Open AI Bridge", "").Click(func() { runtime.WindowShow(a.ctx) })
			tray.toggle = systray.AddMenuItem("Stop bridge", "")
			tray.toggle.Click(func() {
				if a.State().Running {
					a.Stop()
				} else {
					a.Start()
				}
			})
			systray.AddSeparator()
			systray.AddMenuItem("Quit AI Bridge", "").Click(func() {
				systray.Quit()
				runtime.Quit(a.ctx)
			})
			tray.mu.Unlock()
			UpdateTray(tray.running, tray.active, tray.text)
		}, nil)
	}()
}

// UpdateTray shows the bridge state in the tooltip and menu; calls before the tray is ready are replayed.
func UpdateTray(running bool, active int, status string) {
	tray.mu.Lock()
	defer tray.mu.Unlock()
	tray.running, tray.active, tray.text = running, active, status
	if tray.status == nil {
		return
	}
	tip := "AI Bridge: " + status
	if active > 0 {
		tip += " (" + strconv.Itoa(active) + " running)"
	}
	systray.SetTooltip(tip)
	tray.status.SetTitle(status)
	if running {
		tray.toggle.SetTitle("Stop bridge")
	} else {
		tray.toggle.SetTitle("Start bridge")
	}
}
