#!/usr/bin/env python3
import os
import sys
import time
import argparse
import io
import json
import socket
import base64
import threading
import webbrowser
from http.server import BaseHTTPRequestHandler, HTTPServer
from socketserver import ThreadingMixIn

# Global State in the Local Backend Process
latest_frame = None
latest_frame_time = 0.0
latest_width = 1920
latest_height = 1080

# SSH State variables
ssh_client = None
ssh_xdotool_stdin = None
capture_thread = None
capture_running = threading.Event()
remote_input_enabled = False
remote_xauthority = "~/.Xauthority"

active_display = ":0"
active_quality = 75
active_fps = 15  # Optimized default framerate to save remote CPU and network bandwidth

# Dynamic path to connection history file
HISTORY_FILE = os.path.expanduser("~/.remote_viewer_history.json")

# P2P Guest Approval Whitelist State
pending_requests = {}  # client_ip -> guest_name
approved_ips = set()   # whitelisted client_ips
tailscale_auth_url = None

# Helper to check loopback IP ownership
def is_owner(client_ip):
    return client_ip in ("127.0.0.1", "::1", "localhost")

# Check if tailscale CLI is installed on machine
def check_tailscale_installed():
    import shutil
    return shutil.which("tailscale") is not None

# Get Tailscale status and IPs
def get_tailscale_status():
    import subprocess
    if not check_tailscale_installed():
        return {"installed": False, "connected": False, "ip": None, "name": socket.gethostname()}
    try:
        res = subprocess.run(["tailscale", "status", "--json"], capture_output=True, text=True, timeout=3)
        if res.returncode == 0:
            data = json.loads(res.stdout)
            self_node = data.get("Self", {})
            connected = data.get("BackendState") == "Running"
            ips = self_node.get("TailscaleIPs", [])
            ip = ips[0] if ips else None
            name = self_node.get("HostName", socket.gethostname())
            return {
                "installed": True,
                "connected": connected,
                "ip": ip,
                "name": name,
                "backend_state": data.get("BackendState")
            }
    except Exception:
        pass
    
    try:
        res = subprocess.run(["tailscale", "ip"], capture_output=True, text=True, timeout=2)
        if res.returncode == 0:
            ip = res.stdout.strip()
            return {"installed": True, "connected": True, "ip": ip, "name": socket.gethostname()}
    except Exception:
        pass
        
    return {"installed": True, "connected": False, "ip": None, "name": socket.gethostname()}

# Install Tailscale in the background
def install_tailscale_background():
    def run_installer():
        print("[Tailscale Install] Bootstrapping installer command...")
        import subprocess
        try:
            cmd = "curl -fsSL https://tailscale.com/install.sh | sh"
            subprocess.run(cmd, shell=True, capture_output=True, text=True)
            print("[Tailscale Install] Installation script completed successfully.")
        except Exception as e:
            print(f"[Tailscale Install] Error running installer: {e}")
            
    t = threading.Thread(target=run_installer, daemon=True)
    t.start()

# Connect to Tailscale account in background
def tailscale_login_background():
    global tailscale_auth_url
    def run_login():
        global tailscale_auth_url
        print("[Tailscale Login] Running tailscale up --ssh...")
        import subprocess
        try:
            # Run tailscale up --ssh and read output to extract OAuth login URL
            proc = subprocess.Popen(["tailscale", "up", "--ssh"], stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
            
            while proc.poll() is None:
                line = proc.stderr.readline()
                if not line:
                    line = proc.stdout.readline()
                if not line:
                    time.sleep(0.1)
                    continue
                
                print(f"[Tailscale Login Output] {line.strip()}")
                if "https://login.tailscale.com" in line:
                    words = line.split()
                    for w in words:
                        if w.startswith("https://login.tailscale.com"):
                            tailscale_auth_url = w.strip()
                            print(f"[Tailscale Login] Found OAuth Login URL: {tailscale_auth_url}")
                            break
            proc.wait()
            print("[Tailscale Login] Process exited.")
        except Exception as e:
            print(f"[Tailscale Login] Error running tailscale up: {e}")
            
    tailscale_auth_url = None
    t = threading.Thread(target=run_login, daemon=True)
    t.start()

# Threading HTTP Server to handle streaming and inputs concurrently
class ThreadingHTTPServer(ThreadingMixIn, HTTPServer):
    daemon_threads = True

# Helper to construct base64 unbuffered remote python capture agent
def get_remote_capture_code(display, quality, fps):
    code = f"""import os, sys, time, io
os.environ['DISPLAY'] = '{display}'
try:
    import mss
    sct = mss.mss()
except Exception as e:
    sys.stderr.write(f"[Remote Capture Error] mss init failed: {{e}}\\n")
    sys.stderr.flush()
    sct = None
from PIL import Image

last_capture = 0
interval = 1.0 / {fps}

while True:
    now = time.time()
    if now - last_capture < interval:
        time.sleep(max(0.001, interval - (now - last_capture)))
        continue
    last_capture = time.time()
    
    img = None
    if sct:
        try:
            monitor = sct.monitors[1]
            sct_img = sct.grab(monitor)
            img = Image.frombytes('RGB', sct_img.size, sct_img.bgra, 'raw', 'BGRX')
        except Exception as e:
            sys.stderr.write(f"[Remote Capture Error] mss grab failed: {{e}}\\n")
            sys.stderr.flush()
            pass
    if not img:
        try:
            from PIL import ImageGrab
            img = ImageGrab.grab()
        except Exception as e:
            sys.stderr.write(f"[Remote Capture Error] ImageGrab grab failed: {{e}}\\n")
            sys.stderr.flush()
            pass
    if not img:
        try:
            import subprocess
            res = subprocess.run(['grim', '-'], capture_output=True)
            if res.returncode == 0:
                img = Image.open(io.BytesIO(res.stdout))
            else:
                sys.stderr.write(f"[Remote Capture Error] grim failed code {{res.returncode}}\\n")
                sys.stderr.flush()
        except Exception as e:
            sys.stderr.write(f"[Remote Capture Error] grim failed: {{e}}\\n")
            sys.stderr.flush()
            pass
            
    if img:
        try:
            w, h = img.size
            buf = io.BytesIO()
            img.save(buf, format='JPEG', quality={quality})
            jpeg = buf.getvalue()
            
            # Protocol: 2-byte width, 2-byte height, 4-byte length, JPEG bytes
            sys.stdout.buffer.write(w.to_bytes(2, 'big'))
            sys.stdout.buffer.write(h.to_bytes(2, 'big'))
            sys.stdout.buffer.write(len(jpeg).to_bytes(4, 'big'))
            sys.stdout.buffer.write(jpeg)
            sys.stdout.buffer.flush()
        except Exception:
            pass
    else:
        time.sleep(0.1)
        
    # SAFETY CPU RELIEF: Guaranteed sleep to prevent remote Pi CPU from ever saturating
    time.sleep(0.015)
"""
    return base64.b64encode(code.encode('utf-8')).decode('utf-8')

# Dynamic Screen Frame Reader from SSH Stdout with Buffer Bloat Fast-Forwarding
def remote_frame_reader(stdout_stream, running_event):
    global latest_frame, latest_frame_time, latest_width, latest_height
    
    print("[SSH Reader] Dynamic capture stream started.")
    try:
        while running_event.is_set():
            # 1. Read width (2 bytes)
            w_bytes = stdout_stream.read(2)
            if not w_bytes or len(w_bytes) < 2:
                print("[SSH Reader] Remote stream closed (EOF w).")
                break
            w = int.from_bytes(w_bytes, 'big')
            
            # 2. Read height (2 bytes)
            h_bytes = stdout_stream.read(2)
            if not h_bytes or len(h_bytes) < 2:
                print("[SSH Reader] Remote stream closed (EOF h).")
                break
            h = int.from_bytes(h_bytes, 'big')
            
            # 3. Read length (4 bytes)
            len_bytes = stdout_stream.read(4)
            if not len_bytes or len(len_bytes) < 4:
                print("[SSH Reader] Remote stream closed (EOF len).")
                break
            length = int.from_bytes(len_bytes, 'big')
            
            # 4. Read JPEG bytes (variable size `length`)
            jpeg_bytes = b''
            while len(jpeg_bytes) < length and running_event.is_set():
                chunk = stdout_stream.read(length - len(jpeg_bytes))
                if not chunk:
                    break
                jpeg_bytes += chunk
                
            if len(jpeg_bytes) == length:
                latest_frame = jpeg_bytes
                latest_width = w
                latest_height = h
                latest_frame_time = time.time()
                
            # BUFFER BLOAT FAST-FORWARD ELIMINATION:
            # If the SSH channel buffer has more data waiting, we are running behind!
            # Loop and read all pending frames immediately, keeping only the absolute latest frame.
            # This completely destroys network accumulated latency and guarantees sub-100ms click reaction!
            while stdout_stream.channel.recv_ready() and running_event.is_set():
                # Read next frame immediately
                w_bytes = stdout_stream.read(2)
                if not w_bytes or len(w_bytes) < 2:
                    break
                w = int.from_bytes(w_bytes, 'big')
                
                h_bytes = stdout_stream.read(2)
                if not h_bytes or len(h_bytes) < 2:
                    break
                h = int.from_bytes(h_bytes, 'big')
                
                len_bytes = stdout_stream.read(4)
                if not len_bytes or len(len_bytes) < 4:
                    break
                length = int.from_bytes(len_bytes, 'big')
                
                jpeg_bytes = b''
                while len(jpeg_bytes) < length and running_event.is_set():
                    chunk = stdout_stream.read(length - len(jpeg_bytes))
                    if not chunk:
                        break
                    jpeg_bytes += chunk
                    
                if len(jpeg_bytes) == length:
                    # Overwrite and retain only the latest frame!
                    latest_frame = jpeg_bytes
                    latest_width = w
                    latest_height = h
                    latest_frame_time = time.time()
                
    except Exception as e:
        print(f"[SSH Reader] Exception in reader: {e}")
    finally:
        print("[SSH Reader] Reader terminated.")

# Thread to read remote xdotool standard error diagnostic logs
def remote_stderr_reader(stderr_stream, running_event):
    try:
        while running_event.is_set():
            line = stderr_stream.readline()
            if not line:
                break
            print(f"[Remote xdotool Error] {line.strip()}")
    except Exception:
        pass

# Thread to read remote capture agent standard error diagnostic logs
def remote_capture_stderr_reader(stderr_stream, running_event):
    try:
        while running_event.is_set():
            line = stderr_stream.readline()
            if not line:
                break
            print(f"[Remote Capture Error] {line.strip()}")
    except Exception:
        pass

# PyInstaller compatibility helper to find bundled data files
def get_resource_path(relative_path):
    try:
        # PyInstaller temporary extraction directory
        base_path = sys._MEIPASS
    except AttributeError:
        base_path = os.path.abspath(".")
    return os.path.join(base_path, relative_path)

# Connection profiles history loaders (Saved connections)
def load_history():
    if not os.path.exists(HISTORY_FILE):
        return []
    try:
        with open(HISTORY_FILE, "r") as f:
            return json.load(f)
    except Exception as e:
        print(f"[History] Error loading history file: {e}")
        return []

def save_history(target, display, rotation, key_path):
    history = load_history()
    
    # Filter out exact duplicates
    history = [p for p in history if p.get("target") != target or p.get("display") != display]
    
    # Construct connection profile (passwords are strictly never stored!)
    profile = {
        "target": target,
        "display": display,
        "rotation": rotation,
        "key_path": key_path if key_path else None
    }
    
    # Prepend new profile to front of connection history (MRU order)
    history.insert(0, profile)
    
    # Caps connections list at maximum of 6 profiles
    history = history[:6]
    
    try:
        with open(HISTORY_FILE, "w") as f:
            json.dump(history, f, indent=4)
        print(f"[History] Saved connection profile '{target}' successfully.")
    except Exception as e:
        print(f"[History] Error saving history file: {e}")

# Cleanup SSH session details cleanly
def cleanup_ssh():
    global ssh_client, ssh_xdotool_stdin, capture_thread, capture_running, remote_input_enabled
    
    # 1. Stop capture reader thread
    capture_running.clear()
    remote_input_enabled = False
    
    # 2. Close input stream channel
    if ssh_xdotool_stdin:
        try:
            ssh_xdotool_stdin.close()
        except Exception:
            pass
        ssh_xdotool_stdin = None
        
    # 3. Close central client socket
    if ssh_client:
        try:
            ssh_client.close()
        except Exception:
            pass
        ssh_client = None
        
    print("[SSH Cleanup] Disconnected from target Raspberry Pi.")

# Helper to check and automatically install missing remote dependencies (xdotool, mss, Pillow)
def verify_and_install_remote_dependencies(client, username, password):
    print("[SSH Client] Checking remote dependencies (xdotool, mss, Pillow)...")
    
    # 1. Check Python packages (mss, Pillow)
    _, stdout_py, _ = client.exec_command("python3 -c 'import mss, PIL' 2>/dev/null")
    if stdout_py.channel.recv_exit_status() != 0:
        print("[SSH Client] Remote Python dependencies (mss/Pillow) missing. Attempting automatic installation...")
        
        # Try pip user installation first
        pip_cmd = "python3 -m pip install mss Pillow --break-system-packages --user"
        _, stdout_pip, _ = client.exec_command(pip_cmd)
        if stdout_pip.channel.recv_exit_status() != 0:
            print("[SSH Client] Pip installation failed. Attempting system package installation via apt-get...")
            
            # Fallback to apt-get
            if password:
                escaped_pass = password.replace("'", "'\\''")
                apt_cmd = f"echo '{escaped_pass}' | sudo -S apt-get update && echo '{escaped_pass}' | sudo -S apt-get install -y python3-pip python3-pil python3-mss"
            else:
                apt_cmd = "sudo -n apt-get update && sudo -n apt-get install -y python3-pip python3-pil python3-mss"
            
            _, stdout_apt, _ = client.exec_command(apt_cmd)
            stdout_apt.channel.recv_exit_status()
        else:
            print("[SSH Client] Remote Python dependencies installed successfully via pip!")
            
    # 2. Check/Install xdotool
    _, stdout_x, _ = client.exec_command("which xdotool")
    if stdout_x.channel.recv_exit_status() != 0:
        print("[SSH Client] 'xdotool' input simulator is missing. Attempting automatic installation...")
        if password:
            escaped_pass = password.replace("'", "'\\''")
            apt_cmd = f"echo '{escaped_pass}' | sudo -S apt-get update && echo '{escaped_pass}' | sudo -S apt-get install -y xdotool"
        else:
            apt_cmd = "sudo -n apt-get update && sudo -n apt-get install -y xdotool"
            
        _, stdout_apt, _ = client.exec_command(apt_cmd)
        if stdout_apt.channel.recv_exit_status() == 0:
            print("[SSH Client] 'xdotool' installed successfully!")
        else:
            print("[Warning] Could not install 'xdotool' automatically. Inputs will be disabled unless installed manually.")
    else:
        print("[SSH Client] 'xdotool' is already installed.")

# Connect to Raspberry Pi via SSH
def connect_ssh(host, username, password, key_path, display, quality, fps):
    global ssh_client, ssh_xdotool_stdin, capture_thread, capture_running, active_display, active_quality, active_fps, remote_input_enabled, remote_xauthority
    
    # Secure cleanup first
    cleanup_ssh()
    
    import paramiko
    
    active_display = display
    active_quality = quality
    active_fps = fps
    
    client = paramiko.SSHClient()
    client.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    
    try:
        print(f"[SSH Client] Connecting to {username}@{host}...")
        
        # Determine Authentication Method
        if key_path:
            # Custom Private Key file authentication
            abs_key_path = os.path.expanduser(key_path)
            print(f"[SSH Client] Authenticating using custom private key: {abs_key_path}")
            client.connect(host, username=username, key_filename=abs_key_path, timeout=10)
            
        elif password:
            # Password authentication
            print("[SSH Client] Authenticating using password...")
            client.connect(host, username=username, password=password, timeout=10)
            
        else:
            # Passwordless: Search default keys (~/.ssh/id_rsa, etc.) and active SSH Agent
            print("[SSH Client] Attempting passwordless default SSH keys & SSH Agent search...")
            client.connect(host, username=username, allow_agent=True, look_for_keys=True, timeout=10)
            
        # Connection established, store client
        ssh_client = client
        
        # Start capture event flag
        capture_running.set()
        
        # Auto-verify and install remote dependencies
        verify_and_install_remote_dependencies(client, username, password)
        
        # Detect correct Xauthority path dynamically on the remote system (e.g., GDM vs home directory)
        _, stdout_xauth, _ = client.exec_command(
            "if [ -f /run/user/$(id -u)/gdm/Xauthority ]; then echo /run/user/$(id -u)/gdm/Xauthority; else echo ~/.Xauthority; fi"
        )
        remote_xauthority = stdout_xauth.read().decode().strip()
        if not remote_xauthority:
            home_dir = f"/home/{username}" if username != "root" else "/root"
            remote_xauthority = f"{home_dir}/.Xauthority"
        print(f"[SSH Client] Dynamic Xauthority resolved to: {remote_xauthority}")
        
        # 1. Check if xdotool is installed on remote Pi
        print("[SSH Client] Verifying remote dependencies...")
        stdin_chk, stdout_chk, stderr_chk = client.exec_command("which xdotool")
        exit_status = stdout_chk.channel.recv_exit_status()
        
        if exit_status != 0:
            print("[Warning] 'xdotool' was not found on the remote Raspberry Pi.")
            print("Remote mouse and keyboard control will be disabled.")
            print("To enable inputs, run 'sudo apt install xdotool' on the Raspberry Pi.")
            remote_input_enabled = False
            ssh_xdotool_stdin = None
        else:
            # 2. Resolve Xauthority dynamically and spawn persistent input simulator
            print("[SSH Client] Booting remote persistent xdotool session...")
            xdotool_cmd = f"DISPLAY={display} XAUTHORITY={remote_xauthority} xdotool -"
            stdin, stdout, stderr = client.exec_command(xdotool_cmd)
            ssh_xdotool_stdin = stdin
            remote_input_enabled = True
            
            # Start background thread to output remote xdotool errors to laptop console
            t_err = threading.Thread(
                target=remote_stderr_reader,
                args=(stderr, capture_running),
                daemon=True
            )
            t_err.start()
        
        # 3. Deploy unbuffered Python capture agent
        print("[SSH Client] Launching remote unbuffered capture agent...")
        b64_code = get_remote_capture_code(display, quality, fps)
        cmd = f"DISPLAY={display} XAUTHORITY={remote_xauthority} python3 -u -c \"import base64; exec(base64.b64decode('{b64_code}').decode())\""
        
        c_stdin, c_stdout, c_stderr = client.exec_command(cmd)
        
        # Start read thread
        capture_thread = threading.Thread(
            target=remote_frame_reader,
            args=(c_stdout, capture_running),
            daemon=True
        )
        capture_thread.start()
        
        # Start background thread to output remote capture agent errors to laptop console
        t_cap_err = threading.Thread(
            target=remote_capture_stderr_reader,
            args=(c_stderr, capture_running),
            daemon=True
        )
        t_cap_err.start()
        
        print("[SSH Client] Secure tunneling initialized successfully!")
        return True, "ok"
        
    except paramiko.AuthenticationException:
        cleanup_ssh()
        return False, "SSH Authentication failed: Key file invalid, password incorrect, or public key rejected."
    except socket.timeout:
        cleanup_ssh()
        return False, "Connection timeout: Host unreachable or offline."
    except Exception as e:
        cleanup_ssh()
        return False, f"Connection failure: {str(e)}"

# HTTP Server Request Handler
class StreamingHandler(BaseHTTPRequestHandler):
    def log_message(self, format, *args):
        # Suppress request logs to keep terminal clean
        return

    def do_GET(self):
        global latest_frame, latest_frame_time, latest_width, latest_height, ssh_client, remote_input_enabled
        client_ip = self.client_address[0]
        
        if self.path == "/":
            self.send_response(200)
            self.send_header("Content-Type", "text/html")
            self.end_headers()
            try:
                with open(get_resource_path("index.html"), "rb") as f:
                    self.wfile.write(f.read())
            except FileNotFoundError:
                self.wfile.write(b"index.html not found. Please place it in the same directory.")
                
        elif self.path.startswith("/stream"):
            if not ssh_client:
                self.send_error(400, "No active remote session established.")
                return
                
            # AIRTIGHT P2P SECURITY CHECK
            if not is_owner(client_ip) and client_ip not in approved_ips:
                self.send_error(403, "Access Denied: You must request approval from the host to view this stream.")
                return
                
            self.send_response(200)
            self.send_header("Age", "0")
            self.send_header("Cache-Control", "no-cache, private")
            self.send_header("Pragma", "no-cache")
            self.send_header("Content-Type", "multipart/x-mixed-replace; boundary=frame")
            self.end_headers()
            
            last_sent_time = 0.0
            try:
                while capture_running.is_set():
                    if latest_frame_time > last_sent_time and latest_frame is not None:
                        frame = latest_frame
                        last_sent_time = latest_frame_time
                        
                        self.wfile.write(b"--frame\r\n")
                        self.wfile.write(b"Content-Type: image/jpeg\r\n")
                        self.wfile.write(f"Content-Length: {len(frame)}\r\n\r\n".encode())
                        self.wfile.write(frame)
                        self.wfile.write(b"\r\n")
                    else:
                        time.sleep(0.01)
            except Exception:
                # Connection dropped by client
                pass
                
        elif self.path == "/status":
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            status = {
                "connected": ssh_client is not None,
                "input_enabled": remote_input_enabled,
                "width": latest_width,
                "height": latest_height,
                "fps": active_fps,
                "quality": active_quality,
                "is_owner": is_owner(client_ip),
                "approved": client_ip in approved_ips or is_owner(client_ip)
            }
            self.wfile.write(json.dumps(status).encode())
            
        elif self.path == "/history":
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            # Return saved connection history
            history = load_history()
            self.wfile.write(json.dumps(history).encode())
            
        elif self.path == "/tailscale/status":
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            status = get_tailscale_status()
            status["auth_url"] = tailscale_auth_url
            self.wfile.write(json.dumps(status).encode())
            
        elif self.path == "/guest/active_requests":
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            # Guest requests lists are only accessible for the local host owner
            if not is_owner(client_ip):
                self.wfile.write(b'{"requests": []}')
            else:
                req_list = [{"ip": ip, "name": name} for ip, name in pending_requests.items()]
                self.wfile.write(json.dumps({"requests": req_list}).encode())
                
        elif self.path == "/guest/check_status":
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            is_approved = client_ip in approved_ips or is_owner(client_ip)
            self.wfile.write(json.dumps({"approved": is_approved}).encode())
            
        else:
            self.send_error(404, "File not found")

    def do_POST(self):
        global ssh_xdotool_stdin, ssh_client, active_quality, active_fps, active_display, remote_xauthority
        client_ip = self.client_address[0]
        
        if self.path == "/connect":
            content_length = int(self.headers["Content-Length"])
            body = self.rfile.read(content_length)
            params = json.loads(body.decode())
            
            target = params.get("target", "")
            password = params.get("password", "")
            key_path = params.get("key_path", "")
            display = params.get("display", ":0")
            quality = int(params.get("quality", 75))
            fps = int(params.get("fps", 15))
            rotation = params.get("rotation", "0")
            
            # Parse connection details (username@ip)
            username = "pi"
            host = target
            if "@" in target:
                username, host = target.split("@", 1)
                
            success, message = connect_ssh(host, username, password, key_path, display, quality, fps)
            
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            
            if success:
                # Connection profiles are saved dynamically on successful tunnel establishment
                save_history(target, display, rotation, key_path)
                self.wfile.write(b'{"status": "ok"}')
            else:
                self.wfile.write(json.dumps({"status": "error", "message": message}).encode())
                
        elif self.path == "/disconnect":
            cleanup_ssh()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(b'{"status": "ok"}')
            
        elif self.path == "/tailscale/install":
            install_tailscale_background()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(b'{"status": "ok"}')
            
        elif self.path == "/tailscale/login":
            tailscale_login_background()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(b'{"status": "ok"}')
            
        elif self.path == "/guest/request":
            content_length = int(self.headers["Content-Length"])
            body = self.rfile.read(content_length)
            params = json.loads(body.decode())
            name = params.get("name", "Unknown Guest")
            
            pending_requests[client_ip] = name
            print(f"[P2P Server] Guest Access Request from '{name}' (IP: {client_ip})")
            
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(b'{"status": "pending"}')
            
        elif self.path == "/guest/approve":
            # Guest approvals are only allowed for loopback host owner
            if not is_owner(client_ip):
                self.send_error(403, "Access Denied")
                return
                
            content_length = int(self.headers["Content-Length"])
            body = self.rfile.read(content_length)
            params = json.loads(body.decode())
            target_ip = params.get("ip")
            approve = params.get("approve", False)
            
            if target_ip in pending_requests:
                name = pending_requests.pop(target_ip)
                if approve:
                    approved_ips.add(target_ip)
                    print(f"[P2P Server] Host APPROVED guest screen access: {name} (IP: {target_ip})")
                else:
                    print(f"[P2P Server] Host DENIED guest screen access: {name} (IP: {target_ip})")
                    
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(b'{"status": "ok"}')
            
        elif self.path == "/input":
            # AIRTIGHT P2P INPUT CONTROL SECURITY CHECK
            if not is_owner(client_ip) and client_ip not in approved_ips:
                self.send_error(403, "Access Denied: You must request approval from the host to control this session.")
                return
                
            if not ssh_xdotool_stdin:
                self.send_error(400, "Remote input simulator offline.")
                return
                
            content_length = int(self.headers["Content-Length"])
            body = self.rfile.read(content_length)
            event = json.loads(body.decode())
            
            etype = event.get("type")
            try:
                if etype in ("click", "mousedown", "mouseup", "move"):
                    x = event.get("x")
                    y = event.get("y")
                    
                    if etype == "click":
                        btn = event.get("button", 1)
                        cmd = f"mousemove {x} {y}\nclick {btn}\n"
                    elif etype == "mousedown":
                        btn = event.get("button", 1)
                        cmd = f"mousemove {x} {y}\nmousedown {btn}\n"
                    elif etype == "mouseup":
                        btn = event.get("button", 1)
                        cmd = f"mousemove {x} {y}\nmouseup {btn}\n"
                    elif etype == "move":
                        cmd = f"mousemove {x} {y}\n"
                        
                    ssh_xdotool_stdin.write(cmd)
                    ssh_xdotool_stdin.flush()
                    
                elif etype in ("keydown", "keyup"):
                    key = event.get("key")
                    if key:
                        # Key sym mapping for special chars
                        key_map = {
                            "Ctrl": "Control_L",
                            "Shift": "Shift_L",
                            "Alt": "Alt_L",
                            "Meta": "Super_L",
                            "ArrowUp": "Up",
                            "ArrowDown": "Down",
                            "ArrowLeft": "Left",
                            "ArrowRight": "Right",
                        }
                        key = key_map.get(key, key)
                        cmd = f"{etype} {key}\n"
                        ssh_xdotool_stdin.write(cmd)
                        ssh_xdotool_stdin.flush()
                        
            except Exception as e:
                print(f"[Input Engine] Channel write failure: {e}")
                
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(b'{"status": "ok"}')
            
        elif self.path == "/settings":
            if not ssh_client:
                self.send_error(400, "No active remote session.")
                return
                
            content_length = int(self.headers["Content-Length"])
            body = self.rfile.read(content_length)
            settings = json.loads(body.decode())
            
            quality_changed = False
            fps_changed = False
            
            if "quality" in settings:
                q = max(10, min(100, int(settings["quality"])))
                if q != active_quality:
                    active_quality = q
                    quality_changed = True
            if "fps" in settings:
                f = max(5, min(60, int(settings["fps"])))
                if f != active_fps:
                    active_fps = f
                    fps_changed = True
                    
            # If settings updated, restart the remote agent with new values
            if quality_changed or fps_changed:
                print(f"[Server] Dynamic remote agent update: Quality={active_quality}%, FPS={active_fps}")
                
                # Stop existing reader
                capture_running.clear()
                
                # Re-deploy agent with new parameters
                b64_code = get_remote_capture_code(active_display, active_quality, active_fps)
                cmd = f"DISPLAY={active_display} XAUTHORITY={remote_xauthority} python3 -u -c \"import base64; exec(base64.b64decode('{b64_code}').decode())\""
                
                c_stdin, c_stdout, c_stderr = ssh_client.exec_command(cmd)
                
                # Restart reader thread
                capture_running.set()
                capture_thread = threading.Thread(
                    target=remote_frame_reader,
                    args=(c_stdout, capture_running),
                    daemon=True
                )
                capture_thread.start()
                
                # Restart capture stderr reader thread
                t_cap_err = threading.Thread(
                    target=remote_capture_stderr_reader,
                    args=(c_stderr, capture_running),
                    daemon=True
                )
                t_cap_err.start()
                
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(b'{"status": "ok"}')

# Main Entrypoint
if __name__ == "__main__":
    parser = argparse.ArgumentParser(description="Antigravity Remote Screen Viewer - SSH Client")
    parser.add_argument("--port", type=int, default=5000, help="Local Web server port (default: 5000)")
    parser.add_argument("--no-gui", action="store_true", help="Launch without pywebview native desktop window")
    args = parser.parse_args()

    port = args.port

    # Start Local HTTP server
    server_address = ("", port)
    httpd = ThreadingHTTPServer(server_address, StreamingHandler)
    
    print("\n" + "="*60)
    print(" ANTIGRAVITY SSH REMOTE VIEWER - SUCCESS")
    print("="*60)
    print(f" Server running locally on your laptop.")
    print(f" Port target display: {port}")
    print(" Press Ctrl+C to terminate.")
    print("="*60 + "\n")
    
    # Try launching native desktop WebView interface
    has_webview = False
    if not args.no_gui:
        try:
            import webview
            has_webview = True
        except ImportError:
            print("[GUI] pywebview not installed. Gracefully falling back to default web browser.")
            
    if has_webview:
        # Start server in background thread
        srv_thread = threading.Thread(target=httpd.serve_forever, daemon=True)
        srv_thread.start()
        
        # Start pywebview Desktop GUI Window in main thread (blocks until window closed)
        print("[GUI] Opening native desktop window via pywebview...")
        try:
            webview.create_window(
                title="Antigravity Remote Viewer",
                url=f"http://localhost:{port}",
                width=1280,
                height=720,
                resizable=True,
                background_color="#0b0d13"
            )
            webview.start()
        except Exception as gui_err:
            print(f"[GUI] Error running desktop webview window: {gui_err}")
            print("[GUI] Gracefully falling back to opening system browser...")
            webbrowser.open(f"http://localhost:{port}")
            try:
                httpd.serve_forever()
            except KeyboardInterrupt:
                pass
        finally:
            cleanup_ssh()
            httpd.server_close()
            print("[Main] Cleanup complete. Exit.")
    else:
        # Fallback: automatically open in default web browser tab
        print(f"[Server] Automatically launching: http://localhost:{port}")
        webbrowser.open(f"http://localhost:{port}")
        
        try:
            httpd.serve_forever()
        except KeyboardInterrupt:
            print("\n[Main] Shutting down...")
        finally:
            cleanup_ssh()
            httpd.server_close()
            print("[Main] Cleanup complete. Exit.")
