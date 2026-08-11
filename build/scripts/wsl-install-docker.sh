#!/usr/bin/env bash
# Install Docker Engine inside WSL2 Ubuntu and enable rootless-friendly defaults.
set -euo pipefail

export DEBIAN_FRONTEND=noninteractive
LOG="${HOME}/docker-install.log"
exec > >(tee "$LOG") 2>&1

echo "=== Docker install on $(. /etc/os-release; echo "$PRETTY_NAME") ==="

if command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then
  echo "Docker already working:"
  docker version
  exit 0
fi

# Remove old packages if any
sudo apt-get remove -y docker docker-engine docker.io containerd runc 2>/dev/null || true

sudo apt-get update -qq
sudo apt-get install -y ca-certificates curl gnupg lsb-release

# Official Docker apt repo
sudo install -m 0755 -d /etc/apt/keyrings
if [ ! -f /etc/apt/keyrings/docker.asc ]; then
  sudo curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
  sudo chmod a+r /etc/apt/keyrings/docker.asc
fi

# Ubuntu 26.04 (resolute) may not have a dedicated Docker channel yet — fall back to noble.
. /etc/os-release
CODENAME="${VERSION_CODENAME:-noble}"
case "$CODENAME" in
  resolute|questing|plucky)
    echo "Codename $CODENAME not in Docker apt matrix yet; using noble repo."
    CODENAME="noble"
    ;;
esac

echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu ${CODENAME} stable" \
  | sudo tee /etc/apt/sources.list.d/docker.list > /dev/null

sudo apt-get update -qq
sudo apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin

# WSL: start dockerd (no systemd by default on some setups; try service + manual)
if command -v systemctl >/dev/null 2>&1 && systemctl is-system-running >/dev/null 2>&1; then
  sudo systemctl enable --now docker || true
else
  echo "systemd not fully available — starting dockerd manually for this session"
fi

# Always ensure docker group + user membership
sudo groupadd -f docker
sudo usermod -aG docker "$USER" || true

# Start dockerd if not running
if ! docker info >/dev/null 2>&1; then
  if ! pgrep -x dockerd >/dev/null 2>&1; then
    echo "Starting dockerd in background..."
    sudo mkdir -p /var/run
    # Prefer service if available
    if command -v service >/dev/null 2>&1; then
      sudo service docker start || true
    fi
  fi
  # Wait briefly
  for i in 1 2 3 4 5 6 7 8 9 10; do
    if sudo docker info >/dev/null 2>&1; then
      break
    fi
    sleep 1
  done
fi

# If still not up, launch dockerd explicitly
if ! sudo docker info >/dev/null 2>&1; then
  echo "Launching dockerd via nohup..."
  sudo nohup dockerd > /tmp/dockerd.log 2>&1 &
  for i in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15; do
    if sudo docker info >/dev/null 2>&1; then
      break
    fi
    sleep 1
  done
fi

echo "=== docker version ==="
sudo docker version
echo "=== docker info (summary) ==="
sudo docker info 2>/dev/null | head -30 || true

# Hello-world smoke test
echo "=== smoke test: hello-world ==="
sudo docker run --rm hello-world

# Convenience: auto-start dockerd in .bashrc if not running (WSL common pattern)
MARKER="# koyori-ide-docker-autostart"
if ! grep -qF "$MARKER" "${HOME}/.bashrc" 2>/dev/null; then
  cat >> "${HOME}/.bashrc" <<'BASHRC'

# koyori-ide-docker-autostart
if command -v docker >/dev/null 2>&1; then
  if ! docker info >/dev/null 2>&1; then
    if command -v service >/dev/null 2>&1; then
      sudo service docker start >/dev/null 2>&1 || true
    fi
  fi
fi
BASHRC
  echo "Added docker autostart snippet to ~/.bashrc"
fi

echo ""
echo "DOCKER_INSTALL_OK"
echo "Note: re-open WSL shell (or: newgrp docker) to use docker without sudo."
echo "Log: $LOG"
