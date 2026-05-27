#!/bin/bash

# ==============================================================================
#  🍎 DOUBLE-CLICKABLE MACOS INSTALLATION ENTRYPOINT 🍎
# ==============================================================================
# On macOS, double-clicking a file ending in '.command' automatically opens
# the Terminal and runs the script.
#
# IMPORTANT: macOS defaults the working directory of a .command file to the 
# home directory. We must explicitly change the directory to the folder 
# containing this script.

# Move working directory to the folder containing this command file
cd "$(dirname "$0")"

# Grant executable permission to setup_mac.sh in case it was lost during transfer
chmod +x setup_mac.sh

# Run the clean macOS setup and compilation pipeline
./setup_mac.sh

# Keep Terminal open so the user can read the success message and instructions
echo -e "\nPress any key to close this terminal window..."
read -n 1 -s
