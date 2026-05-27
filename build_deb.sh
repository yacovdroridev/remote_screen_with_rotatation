#!/bin/bash

# Define Harmonious ANSI Color Palettes for Console Outputs
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
NC='\033[0m' # No Color

echo -e "${CYAN}======================================================================${NC}"
echo -e "${CYAN}            📦 UBUNTU REMOTE VIEWER .DEB PACKAGER 📦                  ${NC}"
echo -e "${CYAN}======================================================================${NC}"
echo -e "This script will bundle the compiled binary, a premium launch shortcut,"
echo -e "and our custom target screen icon into a standard, distributable"
echo -e "Ubuntu Debian (${GREEN}.deb${NC}) installer package."
echo -e "${CYAN}----------------------------------------------------------------------${NC}"

# 1. Verify that compiled binary exists
if [ ! -f "dist/remote_viewer" ]; then
    echo -e "${RED}[Error] Compiled standalone binary not found at dist/remote_viewer!${NC}"
    echo -e "Please ensure PyInstaller build succeeded before running this script."
    exit 1
fi

# 2. Setup temporary directory structure
echo -e "${BLUE}[1/4] Preparing Debian build directory structure...${NC}"
BUILD_DIR="build_deb_pkg"
rm -rf "$BUILD_DIR"
mkdir -p "$BUILD_DIR/DEBIAN"
mkdir -p "$BUILD_DIR/usr/bin"
mkdir -p "$BUILD_DIR/usr/share/applications"
mkdir -p "$BUILD_DIR/usr/share/pixmaps"

# 3. Copy application files into place
echo -e "${BLUE}[2/4] Injecting compiled binary and glowing launcher icon...${NC}"
cp dist/remote_viewer "$BUILD_DIR/usr/bin/remote-viewer"
chmod 755 "$BUILD_DIR/usr/bin/remote-viewer"

if [ -f "remote_viewer.png" ]; then
    cp remote_viewer.png "$BUILD_DIR/usr/share/pixmaps/remote-viewer.png"
    chmod 644 "$BUILD_DIR/usr/share/pixmaps/remote-viewer.png"
else
    echo -e "${YELLOW}[Warning] Custom app icon (remote_viewer.png) not found in workspace.${NC}"
    echo -e "Defaulting to system icons."
fi

# 4. Generate DEBIAN control configuration file
echo -e "${BLUE}[3/4] Designing package control configuration metadata...${NC}"
cat <<EOT > "$BUILD_DIR/DEBIAN/control"
Package: remote-viewer
Version: 1.0.0
Section: utils
Priority: optional
Architecture: amd64
Maintainer: Yacov Drori <yacovdrori@gmail.com>
Description: Sleek remote display viewer and input injector for Raspberry Pi over SSH.
 Includes embedded Tailscale mesh VPN setup panel and AnyDesk-style P2P host-modal approvals.
EOT

chmod 644 "$BUILD_DIR/DEBIAN/control"

# 5. Generate Desktop Apps Menu launcher entry shortcut
cat <<EOT > "$BUILD_DIR/usr/share/applications/remote-viewer.desktop"
[Desktop Entry]
Name=Remote Screen Viewer
Comment=Sleek Remote Display Viewer & Controller for Raspberry Pi
Exec=remote-viewer
Icon=remote-viewer
Terminal=false
Type=Application
Categories=Utility;Development;
EOT

chmod 644 "$BUILD_DIR/usr/share/applications/remote-viewer.desktop"

# 6. Build the Debian installer package (.deb)
echo -e "${BLUE}[4/4] Packing folder into standard .deb archive...${NC}"
dpkg-deb --root-owner-group --build "$BUILD_DIR" remote-viewer_1.0.0_amd64.deb

if [ $? -ne 0 ]; then
    echo -e "${RED}[Error] Debian package build failed.${NC}"
    rm -rf "$BUILD_DIR"
    exit 1
fi

# Clean up build folders
rm -rf "$BUILD_DIR"

echo -e "${GREEN}======================================================================${NC}"
echo -e "${GREEN}            🎉 DEBIAN PACKAGE COMPILED SUCCESSFULLY! 🎉               ${NC}"
echo -e "${GREEN}======================================================================${NC}"
echo -e "Your Ubuntu installer package is ready:"
echo -e " 👉 ${CYAN}$(pwd)/remote-viewer_1.0.0_amd64.deb${NC}"
echo -e "\n${YELLOW}How to Install on Ubuntu:${NC}"
echo -e "1. Double-click the ${GREEN}remote-viewer_1.0.0_amd64.deb${NC} file in Files (Finder/Nautilus)"
echo -e "   and click ${BLUE}Install${NC} inside Ubuntu Software / App Center."
echo -e "2. Alternatively, run this terminal install command:"
echo -e "   ${GREEN}sudo dpkg -i remote-viewer_1.0.0_amd64.deb${NC}"
echo -e "\n${YELLOW}How to Launch:${NC}"
echo -e " * Simply search for ${GREEN}\"Remote Screen Viewer\"${NC} in your Ubuntu Apps Menu"
echo -e "   and click the custom glowing target icon to boot instantly!"
echo -e "${GREEN}======================================================================${NC}\n"
