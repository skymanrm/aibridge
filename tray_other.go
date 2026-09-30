//go:build !darwin

package main

// InstallTray and UpdateTray are no-ops outside macOS.
func InstallTray(*App)             {}
func UpdateTray(bool, int, string) {}
