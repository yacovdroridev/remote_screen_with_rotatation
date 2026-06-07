package main

import (
	_ "embed"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

//go:embed index.html
var indexHTML []byte

// Connection Profile Structure (passwords strictly never stored!)
type Profile struct {
	Target    string `json:"target"`
	Display   string `json:"display"`
	Rotation  string `json:"rotation"`
	KeyPath   string `json:"key_path,omitempty"`
	Quality   int    `json:"quality,omitempty"`
	FPS       int    `json:"fps,omitempty"`
	ScaleMode string `json:"scale_mode,omitempty"`
}

// Stored parameters for automatic reconnection (in-memory only, never persisted)
type reconnectParams struct {
	Target   string
	Display  string
	Password string
	KeyPath  string
	Quality  int
	FPS      int
	Rotation int
}

// Thread-Safe Application State Container
type AppState struct {
	sync.Mutex
	sshClient           *ssh.Client
	sshInputStdin       io.WriteCloser
	captureSession      chan struct{} // channel to stop active capture thread
	latestFrame         []byte
	latestWidth         int
	latestHeight        int
	activeDisplay       string
	activeQuality       int
	activeFPS           int
	remoteInputEnabled  bool
	remoteXauthority    string
	pendingRequests     map[string]string // clientIP -> guestName
	approvedIPs         map[string]bool   // clientIP -> true
	tailscaleAuthURL    string
	// Reconnection state
	reconnecting     bool
	reconnectFailed  bool
	reconnectStop    chan struct{} // closed to cancel an in-progress reconnect loop
	lastParams       *reconnectParams
}

var state = &AppState{
	pendingRequests: make(map[string]string),
	approvedIPs:     make(map[string]bool),
}

// Expand path helper (expands ~ to user home dir)
func expandPath(path string) string {
	if strings.HasPrefix(path, "~") {
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, path[1:])
		}
	}
	return path
}

// Check loopback ownership
func isOwner(clientIP string) bool {
	host, _, err := net.SplitHostPort(clientIP)
	if err != nil {
		host = clientIP
	}
	return host == "127.0.0.1" || host == "::1" || host == "localhost"
}

// Get raw client IP without port
func getClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// Load connection history profiles from file
func loadHistory() []Profile {
	home, err := os.UserHomeDir()
	if err != nil {
		return []Profile{}
	}
	historyFile := filepath.Join(home, ".remote_viewer_history.json")
	if _, err := os.Stat(historyFile); os.IsNotExist(err) {
		return []Profile{}
	}

	data, err := os.ReadFile(historyFile)
	if err != nil {
		return []Profile{}
	}

	var history []Profile
	if err := json.Unmarshal(data, &history); err != nil {
		return []Profile{}
	}
	return history
}

// Save connection history profile safely
func saveHistory(p Profile) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	historyFile := filepath.Join(home, ".remote_viewer_history.json")
	history := loadHistory()

	// Filter duplicates
	filtered := []Profile{p}
	for _, entry := range history {
		if entry.Target != p.Target || entry.Display != p.Display {
			filtered = append(filtered, entry)
		}
	}

	// Cap at 6 profiles
	if len(filtered) > 6 {
		filtered = filtered[:6]
	}

	data, err := json.MarshalIndent(filtered, "", "    ")
	if err != nil {
		return
	}
	os.WriteFile(historyFile, data, 0644)
}

// Cleanup SSH Sessions cleanly
func (s *AppState) CleanupSSH() {
	s.Lock()
	defer s.Unlock()

	// Cancel any in-progress reconnect loop
	if s.reconnectStop != nil {
		close(s.reconnectStop)
		s.reconnectStop = nil
	}
	s.reconnecting = false
	s.reconnectFailed = false

	// Close dynamic capture session
	if s.captureSession != nil {
		close(s.captureSession)
		s.captureSession = nil
	}

	// Close persistent input stream
	if s.sshInputStdin != nil {
		s.sshInputStdin.Close()
		s.sshInputStdin = nil
	}

	// Close SSH client
	if s.sshClient != nil {
		s.sshClient.Close()
		s.sshClient = nil
	}

	s.remoteInputEnabled = false
	log.Println("[SSH Cleanup] Disconnected from target Raspberry Pi.")
}

// Read raw frames from SSH stdout capture agent
func (s *AppState) ReadCaptureFrames(stdout io.Reader, stopChan chan struct{}) {
	buf := io.Reader(stdout)
	log.Println("[SSH Reader] Dynamic capture stream started.")

	for {
		select {
		case <-stopChan:
			log.Println("[SSH Reader] Reader stopped.")
			return
		default:
			// Read 2-byte width
			var w uint16
			if err := binary.Read(buf, binary.BigEndian, &w); err != nil {
				return
			}
			// Read 2-byte height
			var h uint16
			if err := binary.Read(buf, binary.BigEndian, &h); err != nil {
				return
			}
			// Read 4-byte JPEG size length
			var length uint32
			if err := binary.Read(buf, binary.BigEndian, &length); err != nil {
				return
			}

			// Read exact length bytes
			jpeg := make([]byte, length)
			if _, err := io.ReadFull(buf, jpeg); err != nil {
				return
			}

			// Cache latest frame in thread-safe state
			s.Lock()
			s.latestFrame = jpeg
			s.latestWidth = int(w)
			s.latestHeight = int(h)
			s.Unlock()
		}
	}
}

// startSSHKeepalive sends SSH-level keepalive requests every 5 seconds.
// If the remote stops responding within 10 seconds the SSH connection is
// force-closed, which unblocks any blocked ReadCaptureFrames call and
// triggers the existing reconnect logic.
// It exits when stopChan is closed (deliberate disconnect / new reconnect).
func (s *AppState) startSSHKeepalive(client *ssh.Client, stopChan chan struct{}) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-stopChan:
			return
		case <-t.C:
			done := make(chan error, 1)
			go func() {
				_, _, err := client.Conn.SendRequest("keepalive@openssh.com", true, nil)
				done <- err
			}()
			select {
			case err := <-done:
				if err != nil {
					log.Printf("[Keepalive] SSH keepalive failed: %v — forcing reconnect", err)
					client.Close()
					return
				}
			case <-time.After(10 * time.Second):
				log.Println("[Keepalive] SSH keepalive timed out — forcing reconnect")
				client.Close()
				return
			case <-stopChan:
				return
			}
		}
	}
}

// startReconnectLoop is called when an unexpected SSH disconnection is detected.
// It retries the connection every 5 seconds for up to 90 seconds.
// If the stop channel is closed (deliberate disconnect) it exits immediately.
func (s *AppState) startReconnectLoop(stopChan chan struct{}) {
	deadline := time.Now().Add(90 * time.Second)
	attempt := 0

	for time.Now().Before(deadline) {
		select {
		case <-stopChan:
			log.Println("[Reconnect] Cancelled by deliberate disconnect.")
			return
		default:
		}

		attempt++
		log.Printf("[Reconnect] Attempt %d — waiting 5s before retry...", attempt)

		// Wait 5s or until cancelled
		select {
		case <-stopChan:
			log.Println("[Reconnect] Cancelled by deliberate disconnect.")
			return
		case <-time.After(5 * time.Second):
		}

		// Read params under lock
		s.Lock()
		p := s.lastParams
		s.Unlock()

		if p == nil {
			log.Println("[Reconnect] No stored params — aborting.")
			break
		}

		log.Printf("[Reconnect] Dialling %s...", p.Target)
		if err := s.doReconnect(p, stopChan); err == nil {
			log.Println("[Reconnect] Successfully reconnected.")
			return
		} else {
			log.Printf("[Reconnect] Attempt %d failed: %v", attempt, err)
		}
	}

	// 90 seconds elapsed without success
	s.Lock()
	s.reconnecting = false
	s.reconnectFailed = true
	s.Unlock()
	log.Println("[Reconnect] Timed out after 90 seconds — giving up.")
}

// doReconnect performs one SSH reconnect attempt and starts capture/input sessions.
func (s *AppState) doReconnect(p *reconnectParams, stopChan chan struct{}) error {
	username := "pi"
	host := p.Target
	if strings.Contains(p.Target, "@") {
		parts := strings.SplitN(p.Target, "@", 2)
		username = parts[0]
		host = parts[1]
	}
	if !strings.Contains(host, ":") {
		host = host + ":22"
	}

	var authMethods []ssh.AuthMethod
	if p.Password != "" {
		authMethods = append(authMethods, ssh.Password(p.Password))
	}
	signers, err := getSSHAuthSigners(p.KeyPath)
	if err == nil && len(signers) > 0 {
		authMethods = append(authMethods, ssh.PublicKeys(signers...))
	}
	if len(authMethods) == 0 {
		return fmt.Errorf("no auth methods available")
	}

	config := &ssh.ClientConfig{
		User:            username,
		Auth:            authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}

	client, err := ssh.Dial("tcp", host, config)
	if err != nil {
		return err
	}

	// Check stop channel — user may have disconnected while we were dialling
	select {
	case <-stopChan:
		client.Close()
		return fmt.Errorf("cancelled")
	default:
	}

	// Resolve Xauthority
	xauth := ""
	xauthSess, err := client.NewSession()
	if err == nil {
		out, _ := xauthSess.Output("if [ -f /run/user/$(id -u)/gdm/Xauthority ]; then echo /run/user/$(id -u)/gdm/Xauthority; else echo ~/.Xauthority; fi")
		xauth = strings.TrimSpace(string(out))
		xauthSess.Close()
	}
	if xauth == "" {
		if username == "root" {
			xauth = "/root/.Xauthority"
		} else {
			xauth = fmt.Sprintf("/home/%s/.Xauthority", username)
		}
	}

	// Tear down previous session cleanly (without clearing lastParams or reconnectStop)
	s.Lock()
	if s.captureSession != nil {
		close(s.captureSession)
		s.captureSession = nil
	}
	if s.sshInputStdin != nil {
		s.sshInputStdin.Close()
		s.sshInputStdin = nil
	}
	if s.sshClient != nil {
		s.sshClient.Close()
	}
	s.sshClient = client
	s.remoteXauthority = xauth
	s.captureSession = make(chan struct{})
	captureChan := s.captureSession
	s.reconnecting = false
	s.reconnectFailed = false
	kaStop := s.reconnectStop
	s.Unlock()

	// Start SSH keepalive for the newly reconnected session
	go s.startSSHKeepalive(client, kaStop)

	// Re-check pyautogui and start input agent
	chkSess, err := client.NewSession()
	inputEnabled := false
	if err == nil {
		chkCmd := fmt.Sprintf("DISPLAY=%s XAUTHORITY=%s python3 -c 'import pyautogui'", p.Display, xauth)
		if errRun := chkSess.Run(chkCmd); errRun == nil {
			inputEnabled = true
		}
		chkSess.Close()
	}
	if inputEnabled {
		inSess, err := client.NewSession()
		if err == nil {
			b64Input := getRemoteInputCode()
			runCmd := fmt.Sprintf("DISPLAY=%s XAUTHORITY=%s python3 -u -c \"import base64; exec(base64.b64decode('%s').decode())\"", p.Display, xauth, b64Input)
			stdin, errIn := inSess.StdinPipe()
			if errIn == nil {
				s.Lock()
				s.sshInputStdin = stdin
				s.remoteInputEnabled = true
				s.Unlock()
				go func() {
					inSess.Run(runCmd)
					inSess.Close()
					s.Lock()
					s.remoteInputEnabled = false
					s.Unlock()
				}()
			}
		}
	}

	// Deploy screen capturer agent
	capSess, err := client.NewSession()
	if err == nil {
		b64Cap := getRemoteCaptureCode(p.Display, p.Quality, p.FPS)
		runCmd := fmt.Sprintf("DISPLAY=%s XAUTHORITY=%s python3 -u -c \"import base64; exec(base64.b64decode('%s').decode())\"", p.Display, xauth, b64Cap)
		stdout, errOut := capSess.StdoutPipe()
		if errOut == nil {
			go func() {
				s.ReadCaptureFrames(stdout, captureChan)
			capSess.Close()
			// Detect unexpected disconnect again
			s.Lock()
			unexpected := s.sshClient != nil && !s.reconnecting
			if unexpected {
				s.reconnecting = true
				s.reconnectFailed = false
				s.latestFrame = nil // clear stale frame so /frame returns 400
				sc := s.reconnectStop
				s.Unlock()
				go s.startReconnectLoop(sc)
			} else {
				s.Unlock()
			}
			}()
			go capSess.Run(runCmd)
		}
	}

	return nil
}


func getRemoteCaptureCode(display string, quality, fps int) string {
	code := fmt.Sprintf(`import os, sys, time, io
os.environ['DISPLAY'] = '%s'
try:
    import mss
    sct = mss.mss()
except Exception as e:
    sys.stderr.write(f"[Remote Capture Error] mss init failed: {e}\n")
    sys.stderr.flush()
    sct = None
from PIL import Image

last_capture = 0
interval = 1.0 / %d

while True:
    now = time.time()
    if now - last_capture < interval:
        time.sleep(max(0.001, interval - (now - last_capture)))
        continue
    last_capture = time.time()
    
    img = None
    if sct:
        try:
            if sct.monitors:
                monitor = sct.monitors[1] if len(sct.monitors) > 1 else sct.monitors[0]
                sct_img = sct.grab(monitor)
                img = Image.frombytes('RGB', sct_img.size, sct_img.bgra, 'raw', 'BGRX')
            else:
                raise Exception("sct.monitors list is empty")
        except Exception as e:
            sys.stderr.write(f"[Remote Capture Error] mss grab failed: {e}\n")
            sys.stderr.flush()
            pass
    if not img:
        try:
            from PIL import ImageGrab
            img = ImageGrab.grab()
        except Exception as e:
            sys.stderr.write(f"[Remote Capture Error] ImageGrab grab failed: {e}\n")
            sys.stderr.flush()
            pass
    if not img:
        try:
            import subprocess
            res = subprocess.run(['grim', '-'], capture_output=True)
            if res.returncode == 0:
                img = Image.open(io.BytesIO(res.stdout))
            else:
                sys.stderr.write(f"[Remote Capture Error] grim failed code {res.returncode}\n")
                sys.stderr.flush()
        except Exception as e:
            sys.stderr.write(f"[Remote Capture Error] grim failed: {e}\n")
            sys.stderr.flush()
            pass
            
    if img:
        try:
            w, h = img.size
            buf = io.BytesIO()
            img.save(buf, format='JPEG', quality=%d)
            jpeg = buf.getvalue()
            
            sys.stdout.buffer.write(w.to_bytes(2, 'big'))
            sys.stdout.buffer.write(h.to_bytes(2, 'big'))
            sys.stdout.buffer.write(len(jpeg).to_bytes(4, 'big'))
            sys.stdout.buffer.write(jpeg)
            sys.stdout.buffer.flush()
        except Exception:
            pass
    else:
        time.sleep(0.1)
        
    time.sleep(0.015)
`, display, fps, quality)
	return base64.StdEncoding.EncodeToString([]byte(code))
}

// Base64 captures persistent remote input pyautogui simulator
func getRemoteInputCode() string {
	code := `import sys, json, pyautogui
pyautogui.FAILSAFE = False
pyautogui.PAUSE = 0.0

button_map = {
    1: 'left',
    2: 'middle',
    3: 'right',
    'left': 'left',
    'middle': 'middle',
    'right': 'right'
}

key_map = {
    "Return": "enter",
    "Enter": "enter",
    "Tab": "tab",
    "BackSpace": "backspace",
    "Backspace": "backspace",
    "Escape": "escape",
    "Up": "up",
    "Down": "down",
    "Left": "left",
    "Right": "right",
    "Control_L": "ctrl",
    "Control_R": "ctrl",
    "Shift_L": "shift",
    "Shift_R": "shift",
    "Alt_L": "alt",
    "Alt_R": "alt",
    "Super_L": "win",
    "Super_R": "win",
    "Control": "ctrl",
    "Shift": "shift",
    "Alt": "alt",
    "Meta": "win",
    "ArrowUp": "up",
    "ArrowDown": "down",
    "ArrowLeft": "left",
    "ArrowRight": "right",
}

for line in sys.stdin:
    try:
        event = json.loads(line.strip())
        etype = event.get("type")
        if not etype:
            continue
            
        if etype == "mousemove" or etype == "move":
            x = event.get("x")
            y = event.get("y")
            if x is not None and y is not None:
                pyautogui.moveTo(x, y)
                
        elif etype == "mousedown":
            x = event.get("x")
            y = event.get("y")
            btn = button_map.get(event.get("button"), "left")
            if x is not None and y is not None:
                pyautogui.moveTo(x, y)
            pyautogui.mouseDown(button=btn)
            
        elif etype == "mouseup":
            x = event.get("x")
            y = event.get("y")
            btn = button_map.get(event.get("button"), "left")
            if x is not None and y is not None:
                pyautogui.moveTo(x, y)
            pyautogui.mouseUp(button=btn)
            
        elif etype == "click":
            x = event.get("x")
            y = event.get("y")
            btn = event.get("button")
            if btn == 4:
                pyautogui.scroll(1)
            elif btn == 5:
                pyautogui.scroll(-1)
            else:
                btn_str = button_map.get(btn, "left")
                if x is not None and y is not None:
                    pyautogui.click(x, y, button=btn_str)
                else:
                    pyautogui.click(button=btn_str)
                    
        elif etype == "scroll":
            clicks = event.get("clicks", 0)
            pyautogui.scroll(clicks)
            
        elif etype in ("keydown", "keyup"):
            key = event.get("key")
            if key:
                pyautogui_key = key_map.get(key, key.lower())
                if etype == "keydown":
                    pyautogui.keyDown(pyautogui_key)
                else:
                    pyautogui.keyUp(pyautogui_key)
                    
        elif etype == "type":
            text = event.get("text", "")
            pyautogui.write(text)
            
    except Exception as e:
        sys.stderr.write(f"[Remote Input Error] {e}\n")
        sys.stderr.flush()
`
	return base64.StdEncoding.EncodeToString([]byte(code))
}

// Helper to resolve dynamic auth signers
func getSSHAuthSigners(keyPath string) ([]ssh.Signer, error) {
	var signers []ssh.Signer

	// 1. Try SSH Agent first
	if authSock := os.Getenv("SSH_AUTH_SOCK"); authSock != "" {
		if conn, err := net.Dial("unix", authSock); err == nil {
			agentClient := agent.NewClient(conn)
			if agentSigners, err := agentClient.Signers(); err == nil {
				signers = append(signers, agentSigners...)
			}
		}
	}

	// 2. Try explicitly provided private key file
	if keyPath != "" {
		expanded := expandPath(keyPath)
		if data, err := os.ReadFile(expanded); err == nil {
			if signer, err := ssh.ParsePrivateKey(data); err == nil {
				signers = append(signers, signer)
			}
		}
	}

	// 3. Fallback default SSH keys search
	defaults := []string{"~/.ssh/id_rsa", "~/.ssh/id_dsa", "~/.ssh/id_ecdsa", "~/.ssh/id_ed25519"}
	for _, defPath := range defaults {
		expanded := expandPath(defPath)
		if data, err := os.ReadFile(expanded); err == nil {
			if signer, err := ssh.ParsePrivateKey(data); err == nil {
				signers = append(signers, signer)
			}
		}
	}

	if len(signers) == 0 {
		return nil, fmt.Errorf("no valid signers resolved")
	}
	return signers, nil
}

// Check remote python capturing dependencies and perform auto-installation if required
func verifyAndInstallDependencies(client *ssh.Client, password string) {
	log.Println("[SSH Client] Checking remote dependencies (pyautogui, mss, Pillow)...")

	session, err := client.NewSession()
	if err != nil {
		return
	}
	defer session.Close()

	// Verify Pillow & mss & pyautogui availability
	checkCmd := "python3 -c 'import mss, PIL; import importlib.util; exit(0 if importlib.util.find_spec(\"pyautogui\") else 1)' 2>/dev/null"
	err = session.Run(checkCmd)
	if err == nil {
		log.Println("[SSH Client] All remote dependencies (pyautogui, mss, Pillow) are already installed.")
		return
	}

	log.Println("[SSH Client] Remote dependencies missing. Running auto-installer via pip...")
	session2, err := client.NewSession()
	if err != nil {
		return
	}
	defer session2.Close()

	pipCmd := "python3 -m pip install mss Pillow pyautogui --break-system-packages --user"
	if err2 := session2.Run(pipCmd); err2 == nil {
		log.Println("[SSH Client] Dependencies successfully installed via pip!")
		return
	}

	log.Println("[SSH Client] Pip failed. Attempting system installation via apt-get...")
	session3, err := client.NewSession()
	if err != nil {
		return
	}
	defer session3.Close()

	var aptCmd string
	if password != "" {
		escaped := strings.ReplaceAll(password, "'", "'\\''")
		aptCmd = fmt.Sprintf("echo '%s' | sudo -S apt-get update && echo '%s' | sudo -S apt-get install -y python3-pip python3-pil python3-mss python3-pyautogui", escaped, escaped)
	} else {
		aptCmd = "sudo -n apt-get update && sudo -n apt-get install -y python3-pip python3-pil python3-mss python3-pyautogui"
	}
	session3.Run(aptCmd)
}

// Serve HTTP Request Handlers
func handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	w.Write(indexHTML)
}

func handleFrame(w http.ResponseWriter, r *http.Request) {
	clientIP := getClientIP(r)
	state.Lock()
	approved := state.approvedIPs[clientIP] || isOwner(r.RemoteAddr)
	frame := state.latestFrame
	state.Unlock()

	if !approved {
		http.Error(w, "Access Denied", http.StatusForbidden)
		return
	}

	if frame == nil {
		http.Error(w, "Frame offline", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Content-Length", strconv.Itoa(len(frame)))
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Write(frame)
}

func handleStatus(w http.ResponseWriter, r *http.Request) {
	state.Lock()
	defer state.Unlock()

	clientIP := getClientIP(r)
	approved := state.approvedIPs[clientIP] || isOwner(r.RemoteAddr)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"connected":        state.sshClient != nil,
		"input_enabled":    state.remoteInputEnabled,
		"width":            state.latestWidth,
		"height":           state.latestHeight,
		"fps":              state.activeFPS,
		"quality":          state.activeQuality,
		"is_owner":         isOwner(r.RemoteAddr),
		"approved":         approved,
		"reconnecting":     state.reconnecting,
		"reconnect_failed": state.reconnectFailed,
	})
}

// Delete a connection profile by target+display key
func deleteHistory(target, display string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	history := loadHistory()
	filtered := make([]Profile, 0, len(history))
	for _, p := range history {
		if p.Target != target || p.Display != display {
			filtered = append(filtered, p)
		}
	}
	data, err := json.MarshalIndent(filtered, "", "    ")
	if err != nil {
		return
	}
	os.WriteFile(filepath.Join(home, ".remote_viewer_history.json"), data, 0644)
}

func handleHistory(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case http.MethodGet:
		json.NewEncoder(w).Encode(loadHistory())

	case http.MethodPut:
		// Upsert a profile (create or update by target+display key)
		var p Profile
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		saveHistory(p)
		w.Write([]byte(`{"status":"ok"}`))

	case http.MethodDelete:
		// Delete a profile by target+display
		var req struct {
			Target  string `json:"target"`
			Display string `json:"display"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		deleteHistory(req.Target, req.Display)
		w.Write([]byte(`{"status":"ok"}`))

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func handleConnect(w http.ResponseWriter, r *http.Request) {
	var params struct {
		Target    string `json:"target"`
		Display   string `json:"display"`
		Password  string `json:"password"`
		KeyPath   string `json:"key_path"`
		Quality   int    `json:"quality"`
		FPS       int    `json:"fps"`
		Rotation  int    `json:"rotation"`
		ScaleMode string `json:"scale_mode"`
	}

	log.Println("[Connect] Received /connect request")

	if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
		log.Printf("[Connect] Failed to decode request body: %v", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	log.Printf("[Connect] Target=%q Display=%q Quality=%d FPS=%d Rotation=%d HasPassword=%v KeyPath=%q",
		params.Target, params.Display, params.Quality, params.FPS, params.Rotation,
		params.Password != "", params.KeyPath)

	state.CleanupSSH()

	username := "pi"
	host := params.Target
	if strings.Contains(params.Target, "@") {
		parts := strings.SplitN(params.Target, "@", 2)
		username = parts[0]
		host = parts[1]
	}

	// Re-verify port suffix
	if !strings.Contains(host, ":") {
		host = host + ":22"
	}

	var authMethods []ssh.AuthMethod
	if params.Password != "" {
		authMethods = append(authMethods, ssh.Password(params.Password))
		log.Println("[Connect] Auth: password")
	}

	// Retrieve signers
	signers, err := getSSHAuthSigners(params.KeyPath)
	if err == nil && len(signers) > 0 {
		authMethods = append(authMethods, ssh.PublicKeys(signers...))
		log.Printf("[Connect] Auth: %d SSH key(s)", len(signers))
	} else if err != nil {
		log.Printf("[Connect] SSH key loading warning: %v", err)
	}

	if len(authMethods) == 0 {
		log.Println("[Connect] No auth methods available — rejecting")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"status":  "error",
			"message": "No valid authentication methods (password or keys) available.",
		})
		return
	}

	config := &ssh.ClientConfig{
		User:            username,
		Auth:            authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}

	log.Printf("[SSH Client] Connecting to %s@%s...\n", username, host)
	client, err := ssh.Dial("tcp", host, config)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"status":  "error",
			"message": fmt.Sprintf("SSH connection failure: %v", err),
		})
		return
	}

	state.Lock()
	state.sshClient = client
	state.activeDisplay = params.Display
	state.activeQuality = params.Quality
	state.activeFPS = params.FPS
	state.captureSession = make(chan struct{})
	captureChan := state.captureSession
	// Store params for automatic reconnection (password kept in-memory only)
	state.lastParams = &reconnectParams{
		Target:   params.Target,
		Display:  params.Display,
		Password: params.Password,
		KeyPath:  params.KeyPath,
		Quality:  params.Quality,
		FPS:      params.FPS,
		Rotation: params.Rotation,
	}
	state.reconnectStop = make(chan struct{})
	state.reconnecting = false
	state.reconnectFailed = false
	kaStop := state.reconnectStop
	state.Unlock()

	// Start SSH keepalive — detects silent network drops that TCP alone misses
	go state.startSSHKeepalive(client, kaStop)

	// Install remote deps asynchronously
	verifyAndInstallDependencies(client, params.Password)

	// Resolve Xauthority dynamically
	xauthSess, err := client.NewSession()
	var xauth string
	if err == nil {
		out, _ := xauthSess.Output("if [ -f /run/user/$(id -u)/gdm/Xauthority ]; then echo /run/user/$(id -u)/gdm/Xauthority; else echo ~/.Xauthority; fi")
		xauth = strings.TrimSpace(string(out))
		xauthSess.Close()
	}
	if xauth == "" {
		if username == "root" {
			xauth = "/root/.Xauthority"
		} else {
			xauth = fmt.Sprintf("/home/%s/.Xauthority", username)
		}
	}
	state.Lock()
	state.remoteXauthority = xauth
	state.Unlock()

	// Verify pyautogui remote injection
	chkSess, err := client.NewSession()
	var inputEnabled bool
	if err == nil {
		chkCmd := fmt.Sprintf("DISPLAY=%s XAUTHORITY=%s python3 -c 'import pyautogui'", params.Display, xauth)
		if errRun := chkSess.Run(chkCmd); errRun == nil {
			inputEnabled = true
		}
		chkSess.Close()
	}

	if inputEnabled {
		inSess, err := client.NewSession()
		if err == nil {
			b64Input := getRemoteInputCode()
			runCmd := fmt.Sprintf("DISPLAY=%s XAUTHORITY=%s python3 -u -c \"import base64; exec(base64.b64decode('%s').decode())\"", params.Display, xauth, b64Input)
			stdin, errIn := inSess.StdinPipe()
			if errIn == nil {
				state.Lock()
				state.sshInputStdin = stdin
				state.remoteInputEnabled = true
				state.Unlock()
				// Run input simulator asynchronously
				go func() {
					inSess.Run(runCmd)
					inSess.Close()
					state.Lock()
					state.remoteInputEnabled = false
					state.Unlock()
				}()
			}
		}
	} else {
		log.Println("[Warning] 'pyautogui' was not found on the remote host. Inputs disabled.")
	}

	// Deploy unbuffered screen capturer agent
	capSess, err := client.NewSession()
	if err == nil {
		b64Cap := getRemoteCaptureCode(params.Display, params.Quality, params.FPS)
		runCmd := fmt.Sprintf("DISPLAY=%s XAUTHORITY=%s python3 -u -c \"import base64; exec(base64.b64decode('%s').decode())\"", params.Display, xauth, b64Cap)
		stdout, errOut := capSess.StdoutPipe()
		if errOut == nil {
			go func() {
				state.ReadCaptureFrames(stdout, captureChan)
				capSess.Close()
				// Detect unexpected disconnect (not triggered by CleanupSSH)
				state.Lock()
				unexpected := state.sshClient != nil && !state.reconnecting
				if unexpected {
					state.reconnecting = true
					state.reconnectFailed = false
					state.latestFrame = nil // clear stale frame so /frame returns 400
					sc := state.reconnectStop
					state.Unlock()
					log.Println("[Reconnect] Unexpected disconnect — starting reconnect loop.")
					go state.startReconnectLoop(sc)
				} else {
					state.Unlock()
				}
			}()
			go capSess.Run(runCmd)
		}
	}

	saveHistory(Profile{
		Target:    params.Target,
		Display:   params.Display,
		Rotation:  strconv.Itoa(params.Rotation),
		KeyPath:   params.KeyPath,
		Quality:   params.Quality,
		FPS:       params.FPS,
		ScaleMode: params.ScaleMode,
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func handleDisconnect(w http.ResponseWriter, r *http.Request) {
	state.CleanupSSH()
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status": "ok"}`))
}

func handleInput(w http.ResponseWriter, r *http.Request) {
	clientIP := getClientIP(r)
	state.Lock()
	approved := state.approvedIPs[clientIP] || isOwner(r.RemoteAddr)
	stdin := state.sshInputStdin
	state.Unlock()

	if !approved {
		http.Error(w, "Access Denied", http.StatusForbidden)
		return
	}

	if stdin == nil {
		http.Error(w, "Remote input simulator offline", http.StatusBadRequest)
		return
	}

	// Read event and write directly into SSH stdin channel
	data, err := io.ReadAll(r.Body)
	if err == nil {
		line := strings.TrimSpace(strings.ReplaceAll(string(data), "\n", " ")) + "\n"
		stdin.Write([]byte(line))
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status": "ok"}`))
}

func handleSettings(w http.ResponseWriter, r *http.Request) {
	state.Lock()
	client := state.sshClient
	disp := state.activeDisplay
	xauth := state.remoteXauthority
	state.Unlock()

	if client == nil {
		http.Error(w, "No active session", http.StatusBadRequest)
		return
	}

	var settings struct {
		Quality int `json:"quality"`
		FPS     int `json:"fps"`
	}
	if err := json.NewDecoder(r.Body).Decode(&settings); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	state.Lock()
	if settings.Quality > 0 {
		state.activeQuality = settings.Quality
	}
	if settings.FPS > 0 {
		state.activeFPS = settings.FPS
	}
	q := state.activeQuality
	f := state.activeFPS

	// Close old capture session channel
	if state.captureSession != nil {
		close(state.captureSession)
		state.captureSession = make(chan struct{})
	}
	captureChan := state.captureSession
	state.Unlock()

	// Deploy new capturer agent with revised FPS / Quality settings
	capSess, err := client.NewSession()
	if err == nil {
		b64Cap := getRemoteCaptureCode(disp, q, f)
		runCmd := fmt.Sprintf("DISPLAY=%s XAUTHORITY=%s python3 -u -c \"import base64; exec(base64.b64decode('%s').decode())\"", disp, xauth, b64Cap)
		stdout, errOut := capSess.StdoutPipe()
		if errOut == nil {
			go func() {
				state.ReadCaptureFrames(stdout, captureChan)
				capSess.Close()
				// Detect unexpected disconnect (same as handleConnect / doReconnect)
				state.Lock()
				unexpected := state.sshClient != nil && !state.reconnecting
				if unexpected {
					state.reconnecting = true
					state.reconnectFailed = false
					state.latestFrame = nil
					sc := state.reconnectStop
					state.Unlock()
					log.Println("[Reconnect] Unexpected disconnect detected via settings handler.")
					go state.startReconnectLoop(sc)
				} else {
					state.Unlock()
				}
			}()
			go capSess.Run(runCmd)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status": "ok"}`))
}

// Tailscale Helpers & Integrations
func checkTailscaleInstalled() bool {
	_, err := exec.LookPath("tailscale")
	return err == nil
}

func getTailscaleStatus() map[string]interface{} {
	if !checkTailscaleInstalled() {
		hostname, _ := os.Hostname()
		return map[string]interface{}{"installed": false, "connected": false, "ip": nil, "name": hostname}
	}

	out, err := exec.Command("tailscale", "status", "--json").Output()
	if err == nil {
		var data map[string]interface{}
		if errJson := json.Unmarshal(out, &data); errJson == nil {
			selfNode, _ := data["Self"].(map[string]interface{})
			connected := data["BackendState"] == "Running"
			ipsList, _ := selfNode["TailscaleIPs"].([]interface{})
			var ip interface{}
			if len(ipsList) > 0 {
				ip = ipsList[0]
			}
			hostname, _ := selfNode["HostName"].(string)
			if hostname == "" {
				hostname, _ = os.Hostname()
			}
			return map[string]interface{}{
				"installed":     true,
				"connected":     connected,
				"ip":            ip,
				"name":          hostname,
				"backend_state": data["BackendState"],
			}
		}
	}

	outIp, err := exec.Command("tailscale", "ip").Output()
	if err == nil {
		ip := strings.TrimSpace(string(outIp))
		hostname, _ := os.Hostname()
		return map[string]interface{}{"installed": true, "connected": true, "ip": ip, "name": hostname}
	}

	hostname, _ := os.Hostname()
	return map[string]interface{}{"installed": true, "connected": false, "ip": nil, "name": hostname}
}

func handleTailscaleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	status := getTailscaleStatus()
	state.Lock()
	status["auth_url"] = state.tailscaleAuthURL
	state.Unlock()
	json.NewEncoder(w).Encode(status)
}

func handleTailscaleInstall(w http.ResponseWriter, r *http.Header) {
	// Trigger tailscale installer bootstrap command in background
	go func() {
		log.Println("[Tailscale Install] Bootstrapping installer command...")
		cmd := exec.Command("sh", "-c", "curl -fsSL https://tailscale.com/install.sh | sh")
		cmd.Run()
		log.Println("[Tailscale Install] Installation script completed.")
	}()
}

func handleTailscaleLogin(w http.ResponseWriter, r *http.Request) {
	go func() {
		log.Println("[Tailscale Login] Running tailscale up --ssh...")
		cmd := exec.Command("tailscale", "up", "--ssh")
		stderr, err := cmd.StderrPipe()
		if err != nil {
			return
		}
		cmd.Start()

		buf := make([]byte, 1024)
		for {
			n, errRead := stderr.Read(buf)
			if errRead != nil {
				break
			}
			line := string(buf[:n])
			log.Printf("[Tailscale Login Output] %s\n", strings.TrimSpace(line))
			if strings.Contains(line, "https://login.tailscale.com") {
				words := strings.Fields(line)
				for _, w := range words {
					if strings.HasPrefix(w, "https://login.tailscale.com") {
						state.Lock()
						state.tailscaleAuthURL = strings.TrimSpace(w)
						state.Unlock()
						log.Printf("[Tailscale Login] Found OAuth Login URL: %s\n", state.tailscaleAuthURL)
						break
					}
				}
			}
		}
		cmd.Wait()
		log.Println("[Tailscale Login] Process exited.")
	}()

	state.Lock()
	state.tailscaleAuthURL = ""
	state.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status": "ok"}`))
}

// P2P Guest Approval System
func handleGuestRequest(w http.ResponseWriter, r *http.Request) {
	clientIP := getClientIP(r)
	var params struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	state.Lock()
	state.pendingRequests[clientIP] = params.Name
	state.Unlock()

	log.Printf("[P2P Server] Guest Access Request from '%s' (IP: %s)\n", params.Name, clientIP)

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status": "pending"}`))
}

func handleGuestApprove(w http.ResponseWriter, r *http.Request) {
	if !isOwner(r.RemoteAddr) {
		http.Error(w, "Access Denied", http.StatusForbidden)
		return
	}

	var params struct {
		IP      string `json:"ip"`
		Approve bool   `json:"approve"`
	}
	if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	state.Lock()
	defer state.Unlock()

	if name, ok := state.pendingRequests[params.IP]; ok {
		delete(state.pendingRequests, params.IP)
		if params.Approve {
			state.approvedIPs[params.IP] = true
			log.Printf("[P2P Server] Host APPROVED guest screen access: %s (IP: %s)\n", name, params.IP)
		} else {
			log.Printf("[P2P Server] Host DENIED guest screen access: %s (IP: %s)\n", name, params.IP)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status": "ok"}`))
}

func handleGuestActiveRequests(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if !isOwner(r.RemoteAddr) {
		w.Write([]byte(`{"requests": []}`))
		return
	}

	state.Lock()
	reqList := []map[string]string{}
	for ip, name := range state.pendingRequests {
		reqList = append(reqList, map[string]string{"ip": ip, "name": name})
	}
	state.Unlock()

	json.NewEncoder(w).Encode(map[string]interface{}{"requests": reqList})
}

func handleGuestCheckStatus(w http.ResponseWriter, r *http.Request) {
	clientIP := getClientIP(r)
	state.Lock()
	approved := state.approvedIPs[clientIP] || isOwner(r.RemoteAddr)
	state.Unlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"approved": approved})
}

func freePort(preferred int) int {
	// Try the preferred port first, then let the OS pick a free one
	for _, p := range []int{preferred, 0} {
		l, err := net.Listen("tcp", fmt.Sprintf(":%d", p))
		if err == nil {
			port := l.Addr().(*net.TCPAddr).Port
			l.Close()
			return port
		}
	}
	log.Fatal("[Server] Could not find a free port")
	return 0
}

func main() {
	portFlag := flag.Int("port", 5000, "Local Web server port")
	noGUI := flag.Bool("no-gui", false, "Launch without native window (browser-only mode)")
	flag.Parse()

	port := freePort(*portFlag)

	// Route Handlers Configuration
	http.HandleFunc("/", handleIndex)
	http.HandleFunc("/frame", handleFrame)
	http.HandleFunc("/status", handleStatus)
	http.HandleFunc("/history", handleHistory)
	http.HandleFunc("/connect", handleConnect)
	http.HandleFunc("/disconnect", handleDisconnect)
	http.HandleFunc("/input", handleInput)
	http.HandleFunc("/settings", handleSettings)
	http.HandleFunc("/tailscale/status", handleTailscaleStatus)
	http.HandleFunc("/tailscale/install", func(w http.ResponseWriter, r *http.Request) {
		handleTailscaleInstall(w, &r.Header)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status": "ok"}`))
	})
	http.HandleFunc("/tailscale/login", handleTailscaleLogin)
	http.HandleFunc("/guest/request", handleGuestRequest)
	http.HandleFunc("/guest/approve", handleGuestApprove)
	http.HandleFunc("/guest/active_requests", handleGuestActiveRequests)
	http.HandleFunc("/guest/check_status", handleGuestCheckStatus)

	fmt.Println("\n" + strings.Repeat("=", 60))
	fmt.Printf(" ANTIGRAVITY SSH REMOTE VIEWER - SUCCESS (v2.2.1)\n")
	fmt.Println(strings.Repeat("=", 60))
	fmt.Printf(" Server running locally on your laptop.\n")
	fmt.Printf(" Port target display: %d\n", port)
	fmt.Printf(" Press Ctrl+C to terminate.\n")
	fmt.Println(strings.Repeat("=", 60) + "\n")

	addr := fmt.Sprintf(":%d", port)
	url := fmt.Sprintf("http://localhost:%d", port)

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("[Server] Failed to bind port %d: %v", port, err)
	}

	if *noGUI {
		// Browser-only fallback mode
		go func() {
			time.Sleep(500 * time.Millisecond)
			log.Printf("[Server] Opening in browser: %s\n", url)
			exec.Command("xdg-open", url).Start()
		}()
		log.Fatal(http.Serve(listener, nil))
		return
	}

	// Start HTTP server in background
	go func() {
		if err := http.Serve(listener, nil); err != nil {
			log.Fatalf("[Server] HTTP server error: %v", err)
		}
	}()

	// Poll until the server is actually accepting connections before opening the window
	for i := 0; i < 50; i++ {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", port), 50*time.Millisecond)
		if err == nil {
			conn.Close()
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Open native window (platform-specific implementation)
	openWindow("Antigravity Remote Viewer", url, 1280, 800)
}
