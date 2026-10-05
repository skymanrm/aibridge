// AI Bridge: a menu bar app that lets allowlisted websites use local AI CLIs (Claude Code, Codex, Gemini CLI).
package main

import (
	"embed"
	"log"
	goruntime "runtime"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/menu"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	app := NewApp()
	// macOS gets the app menu for Cmd+Q/C/V and a translucent window; Windows keeps a plain opaque one.
	var appMenu *menu.Menu
	background := &options.RGBA{R: 236, G: 240, B: 244, A: 255}
	if goruntime.GOOS == "darwin" {
		appMenu = menu.NewMenuFromItems(menu.AppMenu(), menu.EditMenu())
		background = &options.RGBA{}
	}
	err := wails.Run(&options.App{
		Title:             "AI Bridge",
		Width:             500,
		Height:            760,
		MinWidth:          420,
		MinHeight:         560,
		AssetServer:       &assetserver.Options{Assets: assets},
		HideWindowOnClose: trayAvailable,
		Menu:              appMenu,
		BackgroundColour:  background,
		OnStartup:         app.startup,
		OnShutdown:        app.shutdown,
		Bind:              []interface{}{app},
		// A second launch shows this window instead of failing to bind the port.
		SingleInstanceLock: &options.SingleInstanceLock{
			UniqueId:               "ru.fanyagin.aibridge",
			OnSecondInstanceLaunch: func(options.SecondInstanceData) { runtime.WindowShow(app.ctx) },
		},
		Mac: &mac.Options{
			TitleBar:             mac.TitleBarHiddenInset(),
			WebviewIsTransparent: true,
			WindowIsTranslucent:  true,
			About:                &mac.AboutInfo{Title: "AI Bridge", Message: "Local Claude Code, Codex and Gemini CLI for your web apps."},
		},
		Windows: &windows.Options{Theme: windows.SystemDefault},
	})
	if err != nil {
		log.Fatal(err)
	}
}
