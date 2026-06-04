//go:build windows

package main

import (
	"fmt"
	"log"
	"os/exec"
)

// openWindow launches Microsoft Edge (or Chrome as fallback) in app mode.
// This gives a clean frameless window experience without requiring CGO/WebView2 headers.
func openWindow(title, url string, width, height int) {
	candidates := [][]string{
		{"cmd", "/C", "start", "msedge", "--app=" + url,
			"--window-size=" + itoa(width) + "," + itoa(height),
			"--disable-extensions", "--no-first-run"},
		{"cmd", "/C", "start", "chrome", "--app=" + url,
			"--window-size=" + itoa(width) + "," + itoa(height),
			"--disable-extensions", "--no-first-run"},
		// Last resort: just open in default browser
		{"cmd", "/C", "start", url},
	}

	for _, args := range candidates {
		cmd := exec.Command(args[0], args[1:]...)
		if err := cmd.Start(); err == nil {
			// Block until the process exits so the HTTP server stays alive
			cmd.Wait()
			return
		}
	}
	log.Println("[Window] Could not open a browser window. Navigate to:", url)
	// Keep the server running so the user can open it manually
	select {}
}

func itoa(n int) string {
	return fmt.Sprintf("%d", n)
}
