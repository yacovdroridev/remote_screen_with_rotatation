# 📺 Antigravity Remote Screen Viewer

A sleek, high-performance, and secure remote desktop screen viewer designed specifically to stream and control active Raspberry Pi displays (such as `:0` NoMachine or primary physical displays) over secure, auto-tunneling SSH. 

This client runs entirely on your local laptop, deploying a tiny unbuffered capture agent to the Pi over SSH and establishing an ultra-low latency input injection pipeline.

---

## ✨ Outstanding Features

* **📦 Premium Desktop WebView App (Electron-like)**: Powered by `pywebview`, the app launches instantly in a clean, native desktop window ( Cocoa on macOS, WebKit2 on Linux), falling back dynamically to your system browser.
* **🔒 AnyDesk-style P2P Session Approval**: Share your screen session securely with associates without giving them your SSH credentials or private keys. Guests simply request access, and a gorgeous glassmorphism modal slides onto your host screen for 1-click **[Approve] / [Deny]** authorization.
* **🌐 Embedded Tailscale Mesh VPN**: Integrated Tailscale mesh panel at the bottom of the portal. It features auto-detection, 1-click background daemon installation, and embedded OAuth login flow (runs `tailscale up --ssh` programmatically and generates interactive login URLs).
* **🔑 Robust Multi-Auth Pipelines**: Full support for standard passwords, custom SSH private key files, and passwordless authentication (scans default system keys like `~/.ssh/id_rsa` and queries active local SSH agents).
* **💾 Saved Connection Profiles**: MRU-ordered connection profiles loaded as clickable chips at the top of the portal. Connections auto-populate in 1-click. **Passwords are strictly never saved** to disk for your security.
* **🎮 Figma-Style Viewport Panning & Centering (1:1 Mode)**: 
  - Fixes native web container scrolling locks.
  - **Spacebar-drag**: Hold down `Space` to turn the cursor into a grabbing hand and pan across high-res screens.
  - **Middle-Click Dragging**: Press and hold your scroll wheel to pan the viewport instantly.
  - Full mouse scroll wheel forwarding to remote web pages.
* **🔄 Zero-CPU Dynamic Rotations**: Switch between `0°`, `90°`, `180°`, and `270°` on-the-fly. Rotations are handled entirely on the local client using GPU-accelerated canvas translations, taking **0% remote CPU**.

---

## 🛠️ Remote Prerequisites (On the Raspberry Pi)

To allow the local laptop to capture the screen and inject inputs:
1. **Input Injection**: Ensure `xdotool` is installed:
   ```bash
   sudo apt update
   sudo apt install xdotool
   ```
2. **Screen Capture Support**: Ensure your Pi has at least one capture mechanism available:
   - **Pillow** (`sudo apt install python3-pil`)
   - **mss** (`pip install mss --break-system-packages --user`)
   - **grim** (Standard Wayland utility, `sudo apt install grim`)

---

## 🚀 Installation & Running

### 🍏 macOS (One-Click / Dummy-Proof)
We have packaged a completely automated, sandboxed script specifically for Macs:
1. Copy/Send the project folder to the macOS machine.
2. In Finder, simply **double-click the `install_mac.command`** file.
3. *A Terminal window will open automatically, safely configure a temporary sandbox environment, install dependencies, compile a native macOS **`RemoteViewer.app`**, and clean up all build caches.*
4. Double-click the generated **`RemoteViewer.app`** to launch it natively just like Slack or Spotify!

### 📦 Ubuntu Desktop (.deb Package)
To install natively on Ubuntu with complete Apps Menu integration:
1. Download the **`remote-viewer_1.0.0_amd64.deb`** installer package from our latest GitHub Release.
2. **Double-click** the `.deb` file in your Ubuntu Files manager to open the App Center and click **Install**.
3. Or install via terminal:
   ```bash
   sudo dpkg -i remote-viewer_1.0.0_amd64.deb
   ```
4. Search for **"Remote Screen Viewer"** in your Ubuntu Activities/Apps Menu, and click our gorgeous target-cyan icon to launch instantly!

### 🐧 Linux / General (Source Setup)
1. Install Python dependencies:
   ```bash
   pip3 install paramiko pywebview Pillow
   ```
2. Run the application:
   ```bash
   python3 remote_viewer.py --port 5000
   ```
3. *Headless option*: If you prefer a pure backend server without launching the local desktop GUI:
   ```bash
   python3 remote_viewer.py --no-gui --port 5000
   ```

---

## 📦 Bundling into a Standalone Executable

You can compile the application into a single, standalone binary executable containing all scripts, assets, and dependencies using PyInstaller:

```bash
python3 -m PyInstaller --onefile --add-data "index.html:." remote_viewer.py
```
*(On Windows, change the colon `:` in `--add-data` to a semicolon `;`: `index.html;.`)*

The resulting single binary will be created in the `dist/` directory:
```bash
./dist/remote_viewer --port 5000
```

---

## 🔒 Tailscale Dynamic P2P Sharing Flow

1. **Host Setup**:
   - Host runs the app locally, clicks **Connect Tailscale** to authorize their machine on their tailnet, and establishes their secure Pi SSH connection.
2. **Guest Connects**:
   - Guest opens their browser on the Mac and navigates to the Host's Tailscale IP address (e.g. `http://100.85.20.40:5000`).
   - Guest inputs their name and clicks **"Request Screen Access"**.
3. **Host Approves**:
   - Host receives a beautiful, animated modal popup directly over their active stream viewer.
   - Host clicks **"Approve"**.
   - Guest's browser is whitelisted by the server and immediately redirected to the active screen stream with flawless inputs!
