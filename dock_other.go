//go:build !darwin

package main

// SetDockBadge is a no-op outside macOS.
func SetDockBadge(int) {}
