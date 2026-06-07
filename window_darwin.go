//go:build darwin

package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
)

// openWindow launches Chrome/Chromium in app mode on macOS and blocks until exit.
// CGO-free: no webview dependency required, so the binary cross-compiles cleanly.
func openWindow(title, url string, width, height int) {
	size := fmt.Sprintf("--window-size=%d,%d", width, height)

	candidates := [][]string{
		{"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"--app=" + url, size, "--disable-extensions", "--no-first-run"},
		{"/Applications/Chromium.app/Contents/MacOS/Chromium",
			"--app=" + url, size, "--disable-extensions", "--no-first-run"},
		{"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
			"--app=" + url, size, "--disable-extensions", "--no-first-run"},
	}

	for _, args := range candidates {
		cmd := exec.Command(args[0], args[1:]...)
		if err := cmd.Start(); err == nil {
			log.Printf("[Window] Launched %s in app mode\n", args[0])
			cmd.Wait()
			return
		}
	}

	// Fallback: open in default browser via macOS `open`
	exec.Command("open", url).Start()
	log.Println("[Window] Opened in default browser. Navigate to:", url)

	// Block until Ctrl+C so the HTTP server stays alive
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
}
