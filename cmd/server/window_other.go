//go:build !windows

package main

func openWindow(url, title string) bool { return false }
