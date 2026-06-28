//go:build windows

package main

import webview2 "github.com/jchv/go-webview2"

func openWindow(url, title string) bool {
	w := webview2.NewWithOptions(webview2.WebViewOptions{
		Debug:     false,
		AutoFocus: true,
		WindowOptions: webview2.WindowOptions{
			Title:  title,
			Width:  1180,
			Height: 800,
			Center: true,
			IconId: 1,
		},
	})
	if w == nil {
		return false
	}
	defer w.Destroy()
	w.Navigate(url)
	w.Run()
	return true
}
