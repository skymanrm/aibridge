//go:build !darwin && !windows

package main

// No tray here, so closing the window quits the app.
const trayAvailable = false

func InstallTray(*App)             {}
func UpdateTray(bool, int, string) {}
