#!/bin/bash
set -e

# Ensure we're running with root privileges for apt/dpkg commands, or use sudo
if [ "$(id -u)" -ne 0 ]; then
  SUDO="sudo"
else
  SUDO=""
fi

echo "Installing GitHub CLI..."
$SUDO mkdir -p -m 755 /etc/apt/keyrings
curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg | $SUDO tee /etc/apt/keyrings/githubcli-archive-keyring.gpg > /dev/null
$SUDO chmod go+r /etc/apt/keyrings/githubcli-archive-keyring.gpg
echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" | $SUDO tee /etc/apt/sources.list.d/github-cli.list > /dev/null
$SUDO apt-get update
$SUDO apt-get install gh -y

echo "Installing git-delta..."
ARCH=$(dpkg --print-architecture)
DELTA_VERSION="0.17.0"
if [ "$ARCH" = "arm64" ]; then
    DELTA_ARCH="arm64"
else
    DELTA_ARCH="amd64"
fi

curl -Ls "https://github.com/dandavison/delta/releases/download/${DELTA_VERSION}/git-delta_${DELTA_VERSION}_${DELTA_ARCH}.deb" -o delta.deb
$SUDO dpkg -i delta.deb
rm delta.deb

echo "Dependencies installed successfully!"