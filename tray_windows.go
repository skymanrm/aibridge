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
	meter          trayMeter
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
			meter := tray.meter
			tray.mu.Unlock()
			UpdateTray(tray.running, tray.active, tray.text)
			UpdateTrayUsage(meter)
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
	setTooltipLocked()
	tray.status.SetTitle(status)
	if running {
		tray.toggle.SetTitle("Stop bridge")
	} else {
		tray.toggle.SetTitle("Start bridge")
	}
}

// UpdateTrayUsage swaps the icon for the usage bars (the app icon when off) and adds the limits to the tooltip.
func UpdateTrayUsage(m trayMeter) {
	tray.mu.Lock()
	defer tray.mu.Unlock()
	tray.meter = m
	if tray.status == nil {
		return
	}
	if len(m.Percents) > 0 {
		systray.SetIcon(meterIconICO(m, 32))
	} else {
		systray.SetIcon(trayIcon)
	}
	setTooltipLocked()
}

// setTooltipLocked needs tray.mu; Windows cuts tooltips at 127 characters.
func setTooltipLocked() {
	tip := "AI Bridge"
	if len(tray.meter.Lines) == 0 {
		tip += ": " + tray.text // the status is in the menu too, so usage lines take its place
	}
	if tray.active > 0 {
		tip += " (" + strconv.Itoa(tray.active) + " running)"
	}
	for _, l := range tray.meter.Lines {
		tip += "\n" + l
	}
	if r := []rune(tip); len(r) > 127 {
		tip = string(r[:127])
	}
	systray.SetTooltip(tip)
}
