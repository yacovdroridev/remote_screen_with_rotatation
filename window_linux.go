//go:build linux

package main

import webview "github.com/webview/webview_go"

// openWindow opens a native webkit2gtk webview window. Must be called on the main thread.
func openWindow(title, url string, width, height int) {
	w := webview.New(true)
	defer w.Destroy()
	w.SetTitle(title)
	w.SetSize(width, height, webview.HintNone)
	w.Navigate(url)
	w.Run()
}
