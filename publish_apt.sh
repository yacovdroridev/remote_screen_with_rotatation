#!/bin/bash
set -e

# ============================================================
#  APT REPOSITORY PUBLISHER
#  Builds .deb, updates the GitHub Pages APT repo, and
#  pushes so users can install/upgrade via:
#    sudo apt update && sudo apt install remote-viewer
#
#  CI mode (GitHub Actions):
#    Set APT_SIGNING_KEY  — armoured GPG private key (gpg --armor --export-secret-keys)
#    Set GITHUB_TOKEN     — automatically provided by Actions, used for git push
# ============================================================

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
NC='\033[0m'

REPO_OWNER="yacovdroridev"
REPO_NAME="remote_screen_with_rotatation"
PAGES_URL="https://${REPO_OWNER}.github.io/${REPO_NAME}"
GPG_KEY_NAME="Remote Viewer APT Signing Key"
GPG_KEY_EMAIL="apt@remote-viewer"
SUITE="stable"
COMPONENT="main"
ARCH="amd64"
WORK_DIR="/tmp/apt-repo-publish-$$"
PROJECT_DIR="$(pwd)"

# Build the authenticated remote URL (works locally and in CI)
if [ -n "${GITHUB_TOKEN:-}" ] && [ -n "${GITHUB_REPOSITORY:-}" ]; then
    REMOTE_URL="https://x-access-token:${GITHUB_TOKEN}@github.com/${GITHUB_REPOSITORY}.git"
else
    REMOTE_URL="$(git remote get-url origin)"
fi

echo -e "${CYAN}================================================================${NC}"
echo -e "${CYAN}           APT REPOSITORY PUBLISHER - Remote Viewer             ${NC}"
echo -e "${CYAN}================================================================${NC}"

# ── Step 0: Detect version from build_deb.sh ────────────────
VERSION=$(grep '^VERSION=' build_deb.sh | cut -d'"' -f2)
DEB_FILE="remote-viewer_${VERSION}_amd64.deb"
echo -e "${BLUE}[0/7] Version: ${GREEN}${VERSION}${NC}"

# ── Step 1: Build the .deb (skip if already built by CI) ────
echo -e "${BLUE}[1/7] Building .deb package...${NC}"
if [ ! -f "$DEB_FILE" ]; then
    bash build_deb.sh > /dev/null
    echo -e "      ${GREEN}✓ ${DEB_FILE} built.${NC}"
else
    echo -e "      ${GREEN}✓ ${DEB_FILE} already exists, skipping build.${NC}"
fi

# ── Step 2: Ensure GPG signing key exists ───────────────────
echo -e "${BLUE}[2/7] Checking GPG signing key...${NC}"

# CI mode: import the key from the environment variable
if [ -n "${APT_SIGNING_KEY:-}" ]; then
    echo -e "      ${BLUE}CI mode: importing GPG key from environment...${NC}"
    echo "$APT_SIGNING_KEY" | gpg --batch --import
fi

GPG_KEY_ID=$(gpg --list-secret-keys --with-colons 2>/dev/null \
    | awk -F: '/^sec/ {print $5; exit}')

if [ -z "$GPG_KEY_ID" ]; then
    echo -e "      ${YELLOW}No GPG key found — generating dedicated signing key...${NC}"
    gpgconf --kill gpg-agent 2>/dev/null || true
    gpg --batch --passphrase '' \
        --quick-gen-key "${GPG_KEY_NAME} <${GPG_KEY_EMAIL}>" rsa4096 sign 0
    GPG_KEY_ID=$(gpg --list-secret-keys --with-colons \
        | awk -F: '/^sec/ {print $5; exit}')
    echo -e "      ${GREEN}✓ Generated key: ${GPG_KEY_ID}${NC}"
else
    echo -e "      ${GREEN}✓ Using key: ${GPG_KEY_ID}${NC}"
fi

# Export armoured public key (users import this once)
gpg --armor --export "$GPG_KEY_ID" > KEY.gpg
echo -e "      ${GREEN}✓ Public key exported to KEY.gpg${NC}"

# ── Step 3: Clone gh-pages into temp dir ────────────────────
echo -e "${BLUE}[3/7] Syncing gh-pages branch...${NC}"
rm -rf "$WORK_DIR"

if git ls-remote --exit-code --heads "$REMOTE_URL" gh-pages > /dev/null 2>&1; then
    git clone --quiet --single-branch --branch gh-pages "$REMOTE_URL" "$WORK_DIR"
    echo -e "      ${GREEN}✓ Cloned existing gh-pages branch.${NC}"
else
    mkdir -p "$WORK_DIR"
    cd "$WORK_DIR"
    git init --quiet
    git remote add origin "$REMOTE_URL"
    git checkout --quiet --orphan gh-pages
    cd "$PROJECT_DIR"
    echo -e "      ${GREEN}✓ Created new orphan gh-pages branch.${NC}"
fi

# ── Step 4: Populate pool and dists ─────────────────────────
echo -e "${BLUE}[4/7] Updating repository pool...${NC}"
POOL_DIR="$WORK_DIR/pool/${COMPONENT}"
DISTS_DIR="$WORK_DIR/dists/${SUITE}/${COMPONENT}/binary-${ARCH}"
mkdir -p "$POOL_DIR" "$DISTS_DIR"

# Copy .deb and public key
cp "$DEB_FILE" "$POOL_DIR/"
cp KEY.gpg "$WORK_DIR/KEY.gpg"

# Keep only the latest .deb for each package name (remove older versions)
ls "$POOL_DIR"/remote-viewer_*_amd64.deb 2>/dev/null \
    | grep -v "$DEB_FILE" \
    | xargs -r rm -f
echo -e "      ${GREEN}✓ Pool updated (older versions pruned).${NC}"

# ── Step 5: Generate Packages index ─────────────────────────
echo -e "${BLUE}[5/7] Generating Packages index...${NC}"
cd "$WORK_DIR"
dpkg-scanpackages --arch "$ARCH" pool/ > "dists/${SUITE}/${COMPONENT}/binary-${ARCH}/Packages"
gzip -9 -k "dists/${SUITE}/${COMPONENT}/binary-${ARCH}/Packages"
echo -e "      ${GREEN}✓ Packages and Packages.gz generated.${NC}"

# ── Step 6: Generate and sign Release file ──────────────────
echo -e "${BLUE}[6/7] Generating and signing Release file...${NC}"
apt-ftparchive \
    -o "APT::FTPArchive::Release::Origin=Remote Viewer" \
    -o "APT::FTPArchive::Release::Label=Remote Viewer" \
    -o "APT::FTPArchive::Release::Suite=${SUITE}" \
    -o "APT::FTPArchive::Release::Codename=${SUITE}" \
    -o "APT::FTPArchive::Release::Architectures=${ARCH}" \
    -o "APT::FTPArchive::Release::Components=${COMPONENT}" \
    -o "APT::FTPArchive::Release::Description=Remote Viewer APT Repository" \
    release "dists/${SUITE}" > "dists/${SUITE}/Release"

gpg --default-key "$GPG_KEY_ID" \
    --batch --yes \
    -abs -o "dists/${SUITE}/Release.gpg" "dists/${SUITE}/Release"

gpg --default-key "$GPG_KEY_ID" \
    --batch --yes \
    --clearsign -o "dists/${SUITE}/InRelease" "dists/${SUITE}/Release"

echo -e "      ${GREEN}✓ Release, Release.gpg, and InRelease signed.${NC}"

# ── Step 7: Commit and push gh-pages ────────────────────────
echo -e "${BLUE}[7/7] Publishing to GitHub Pages...${NC}"
cd "$WORK_DIR"
git config user.name  "$(git -C "$PROJECT_DIR" config user.name  2>/dev/null || echo 'APT Publisher')"
git config user.email "$(git -C "$PROJECT_DIR" config user.email 2>/dev/null || echo 'apt@remote-viewer')"

git add -A
git commit --quiet -m "apt: publish remote-viewer v${VERSION}"
git push --quiet origin gh-pages --force
cd "$PROJECT_DIR"

# Cleanup
rm -rf "$WORK_DIR"

# ── Done ─────────────────────────────────────────────────────
echo -e "${GREEN}================================================================${NC}"
echo -e "${GREEN}           APT REPOSITORY PUBLISHED SUCCESSFULLY!              ${NC}"
echo -e "${GREEN}================================================================${NC}"
echo
echo -e "${YELLOW}Users can install / upgrade with:${NC}"
echo
echo -e "  ${CYAN}# Add GPG key (one-time)${NC}"
echo -e "  curl -fsSL ${PAGES_URL}/KEY.gpg | sudo gpg --dearmor \\"
echo -e "    -o /usr/share/keyrings/remote-viewer.gpg"
echo
echo -e "  ${CYAN}# Add APT source (one-time)${NC}"
echo -e "  echo \"deb [signed-by=/usr/share/keyrings/remote-viewer.gpg] \\"
echo -e "    ${PAGES_URL} stable main\" \\"
echo -e "    | sudo tee /etc/apt/sources.list.d/remote-viewer.list"
echo
echo -e "  ${CYAN}# Install or upgrade${NC}"
echo -e "  sudo apt update && sudo apt install remote-viewer"
echo -e "  ${CYAN}# — or — ${NC}"
echo -e "  sudo apt update && sudo apt upgrade"
echo
echo -e "${GREEN}================================================================${NC}"
