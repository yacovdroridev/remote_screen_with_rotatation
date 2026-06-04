//go:build windows

package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
)

// openWindow launches Microsoft Edge (or Chrome as fallback) in app mode and
// blocks until the user closes the app (Ctrl+C or process signal).
func openWindow(title, url string, width, height int) {
	size := fmt.Sprintf("--window-size=%d,%d", width, height)

	candidates := [][]string{
		{"msedge", "--app=" + url, size, "--disable-extensions", "--no-first-run"},
		{"chrome",  "--app=" + url, size, "--disable-extensions", "--no-first-run"},
		{"chromium","--app=" + url, size, "--disable-extensions", "--no-first-run"},
	}

	launched := false
	for _, args := range candidates {
		cmd := exec.Command(args[0], args[1:]...)
		if err := cmd.Start(); err == nil {
			log.Printf("[Window] Launched %s in app mode\n", args[0])
			launched = true
			// Wait for the browser process to exit
			cmd.Wait()
			return
		}
	}

	if !launched {
		// Last resort: open in default browser via explorer
		exec.Command("explorer", url).Start()
		log.Println("[Window] Opened in default browser. Navigate to:", url)
	}

	// Block until Ctrl+C so the HTTP server stays alive
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
}
