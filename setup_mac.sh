#!/bin/bash

# Define Harmonious ANSI Color Palettes for Premium Console Logs
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
NC='\033[0m' # No Color

echo -e "${CYAN}======================================================================${NC}"
echo -e "${CYAN}             🍎 MACOS REMOTE VIEWER ONE-CLICK SETUP 🍎                ${NC}"
echo -e "${CYAN}======================================================================${NC}"
echo -e "This script will automatically configure Python, install all dependencies"
echo -e "safely in a temporary sandbox, and compile a premium, double-clickable"
echo -e "native macOS App Bundle (${GREEN}RemoteViewer.app${NC}) in this directory."
echo -e "${CYAN}----------------------------------------------------------------------${NC}"

# 1. Verify macOS environment
if [ "$(uname)" != "Darwin" ]; then
    echo -e "${RED}[Error] This script must be run on a macOS system!${NC}"
    exit 1
fi

# 2. Check for Python 3 installation
if ! command -v python3 &> /dev/null; then
    echo -e "${YELLOW}[Warning] Python 3 is not installed or not in PATH!${NC}"
    echo -e "Attempting to trigger macOS developer tools installation..."
    xcode-select --install
    echo -e "${RED}[Action Required] Please follow the Xcode Command Line Tools prompt,${NC}"
    echo -e "and then re-run this setup script once installation is complete."
    exit 1
fi

# 3. Create a temporary sandbox Virtual Environment (avoids system pip constraints!)
echo -e "\n${BLUE}[1/4] Configuring clean sandboxed Python environment...${NC}"
rm -rf .mac_build_env
python3 -m venv .mac_build_env
if [ $? -ne 0 ]; then
    echo -e "${RED}[Error] Failed to create virtual environment sandbox.${NC}"
    exit 1
fi

# Activate the sandbox virtual environment
source .mac_build_env/bin/activate

# 4. Update pip and install required dependencies safely inside sandbox
echo -e "\n${BLUE}[2/4] Fetching required secure libraries...${NC}"
python3 -m pip install --upgrade pip
python3 -m pip install paramiko pywebview Pillow pyinstaller

if [ $? -ne 0 ]; then
    echo -e "${RED}[Error] Library installation failed. Please check internet connection.${NC}"
    deactivate
    exit 1
fi

# 5. Compile into a native double-clickable Mac App Bundle (.app)
echo -e "\n${BLUE}[3/4] Packaging Remote Capture Viewer into native App Bundle...${NC}"
echo -e "${YELLOW}      (This might take a minute, please wait...)${NC}"

# Clean old build artifacts first
rm -rf build dist RemoteViewer.app

# Compile using PyInstaller with '--windowed' / '-w' to generate a native macOS .app
python3 -m PyInstaller \
    --onefile \
    --windowed \
    --add-data "index.html:." \
    --name "RemoteViewer" \
    remote_viewer.py

if [ $? -ne 0 ]; then
    echo -e "${RED}[Error] PyInstaller compilation failed.${NC}"
    deactivate
    exit 1
fi

# 6. Finalize, clean up sandbox, and place double-clickable app in root
echo -e "\n${BLUE}[4/4] Finalizing setup and cleaning up build cache...${NC}"

# Move the double-clickable .app bundle to the root of the folder for ultimate dummy-proof access
if [ -d "dist/RemoteViewer.app" ]; then
    mv dist/RemoteViewer.app ./
    rm -rf dist build RemoteViewer.spec
else
    echo -e "${RED}[Error] App bundle not found in expected output directory.${NC}"
    deactivate
    exit 1
fi

# Clean up sandboxed virtual environment
deactivate
rm -rf .mac_build_env

echo -e "\n${GREEN}======================================================================${NC}"
echo -e "${GREEN}             🎉 MACOS APP BUNDLE CREATED SUCCESSFULLY! 🎉             ${NC}"
echo -e "${GREEN}======================================================================${NC}"
echo -e "You now have a premium native macOS App in this folder:"
echo -e " 👉 ${CYAN}$(pwd)/RemoteViewer.app${NC}"
echo -e "\n${YELLOW}How to Launch:${NC}"
echo -e "1. Simply ${GREEN}double-click RemoteViewer.app${NC} in Finder to run instantly!"
echo -e "2. (Optional) Drag and drop it into your ${BLUE}/Applications${NC} folder to add it to Launchpad."
echo -e "3. Give the entire ${GREEN}RemoteViewer.app${NC} file directly to your associate!"
echo -e "${GREEN}======================================================================${NC}\n"
