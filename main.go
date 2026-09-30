// AI Bridge: a menu bar app that lets allowlisted websites use local AI CLIs (Claude Code, Codex).
package main

import (
	"embed"
	"log"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/menu"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	app := NewApp()
	err := wails.Run(&options.App{
		Title:             "AI Bridge",
		Width:             500,
		Height:            760,
		MinWidth:          420,
		MinHeight:         560,
		AssetServer:       &assetserver.Options{Assets: assets},
		HideWindowOnClose: true,
		Menu:              menu.NewMenuFromItems(menu.AppMenu(), menu.EditMenu()), // keeps Cmd+Q/C/V working without a menu bar
		BackgroundColour:  &options.RGBA{R: 0, G: 0, B: 0, A: 0},
		OnStartup:         app.startup,
		OnShutdown:        app.shutdown,
		Bind:              []interface{}{app},
		Mac: &mac.Options{
			TitleBar:             mac.TitleBarHiddenInset(),
			WebviewIsTransparent: true,
			WindowIsTranslucent:  true,
			About:                &mac.AboutInfo{Title: "AI Bridge", Message: "Local Claude Code and Codex for your web apps."},
		},
	})
	if err != nil {
		log.Fatal(err)
	}
}
