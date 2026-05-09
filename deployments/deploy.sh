#!/usr/bin/env bash
# First-run deploy script for a fresh Ubuntu 24.04 VPS.
#
# What it does:
#   1. Hardens SSH (disables password auth at the server level, just in case).
#   2. Sets up UFW firewall (allow only 22/tcp, 80/tcp, 443/tcp + 443/udp).
#   3. Installs Docker Engine + Compose plugin.
#   4. Clones (or pulls) the FRI repo.
#   5. Walks you through filling in .env.
#   6. Starts the production stack.
#
# Usage on the server (after `ssh root@<your-ip>`):
#   curl -fsSL https://raw.githubusercontent.com/<you>/fri/main/deployments/deploy.sh | bash
# Or copy this file over and run:
#   bash deploy.sh
#
# Re-running is safe: each step checks current state and skips if done.

set -euo pipefail

REPO_URL="${REPO_URL:-}"          # set via env or you'll be prompted
REPO_DIR="${REPO_DIR:-/opt/fri}"
BRANCH="${BRANCH:-main}"

log() { printf '\033[1;36m[deploy]\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[deploy]\033[0m %s\n' "$*" >&2; }
err() { printf '\033[1;31m[deploy]\033[0m %s\n' "$*" >&2; exit 1; }

# ─── 0. Sanity checks ────────────────────────────────────────────────────
[[ $EUID -eq 0 ]] || err "Run as root: sudo bash deploy.sh"
. /etc/os-release
[[ "$ID" == "ubuntu" ]] || warn "Tested on Ubuntu only. You're on $PRETTY_NAME — proceed at your own risk."

# ─── 1. Harden SSH ───────────────────────────────────────────────────────
log "Hardening SSH (PasswordAuthentication no)..."
sed -i.bak 's/^#*PasswordAuthentication.*/PasswordAuthentication no/' /etc/ssh/sshd_config
sed -i 's/^#*PermitRootLogin.*/PermitRootLogin prohibit-password/' /etc/ssh/sshd_config
systemctl reload ssh || systemctl reload sshd

# ─── 2. UFW firewall ─────────────────────────────────────────────────────
if ! command -v ufw >/dev/null; then
  log "Installing ufw..."
  apt-get update -qq
  apt-get install -y -qq ufw
fi
log "Configuring firewall (allow 22, 80, 443; deny everything else)..."
ufw --force reset >/dev/null
ufw default deny incoming
ufw default allow outgoing
ufw allow 22/tcp comment 'SSH'
ufw allow 80/tcp comment 'HTTP (Lets Encrypt + redirect)'
ufw allow 443/tcp comment 'HTTPS'
ufw allow 443/udp comment 'HTTP/3'
ufw --force enable
ufw status verbose

# ─── 3. Docker ───────────────────────────────────────────────────────────
if ! command -v docker >/dev/null; then
  log "Installing Docker Engine + Compose plugin..."
  apt-get update -qq
  apt-get install -y -qq ca-certificates curl gnupg
  install -m 0755 -d /etc/apt/keyrings
  curl -fsSL https://download.docker.com/linux/ubuntu/gpg | gpg --dearmor -o /etc/apt/keyrings/docker.gpg
  chmod a+r /etc/apt/keyrings/docker.gpg
  # shellcheck disable=SC1091
  . /etc/os-release
  echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/ubuntu $VERSION_CODENAME stable" \
    > /etc/apt/sources.list.d/docker.list
  apt-get update -qq
  apt-get install -y -qq docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
  systemctl enable --now docker
fi
log "Docker version: $(docker --version)"
log "Compose version: $(docker compose version)"

# ─── 4. Repo ─────────────────────────────────────────────────────────────
if [[ ! -d "$REPO_DIR/.git" ]]; then
  if [[ -z "$REPO_URL" ]]; then
    read -rp "Git repo URL (https://github.com/you/fri.git): " REPO_URL
  fi
  log "Cloning $REPO_URL → $REPO_DIR..."
  git clone --branch "$BRANCH" "$REPO_URL" "$REPO_DIR"
else
  log "Repo already at $REPO_DIR — pulling latest $BRANCH..."
  git -C "$REPO_DIR" fetch --all
  git -C "$REPO_DIR" checkout "$BRANCH"
  git -C "$REPO_DIR" pull --ff-only
fi

cd "$REPO_DIR/deployments"

# ─── 5. .env ─────────────────────────────────────────────────────────────
if [[ ! -f .env ]]; then
  cp .env.prod.example .env
  warn ".env created from template. You MUST fill in the secrets:"
  warn "  - PUBLIC_HOSTNAME (your domain or IP)"
  warn "  - ACME_EMAIL (for Let's Encrypt)"
  warn "  - POSTGRES_PASSWORD (auto-generated below if you accept)"
  warn "  - API_FOOTBALL_KEY / YOUTUBE_API_KEY / MEDIASTACK_API_KEY"
  echo
  read -rp "Generate a strong POSTGRES_PASSWORD now and write it to .env? [Y/n] " yn
  if [[ "${yn:-Y}" =~ ^[Yy]$ ]]; then
    pw=$(openssl rand -hex 24)
    sed -i "s|^POSTGRES_PASSWORD=.*|POSTGRES_PASSWORD=$pw|" .env
    log "POSTGRES_PASSWORD set."
  fi
  echo
  warn "Now edit .env to fill the rest, then re-run this script."
  warn "  nano $REPO_DIR/deployments/.env"
  exit 0
fi

# Quick sanity check that the required vars are non-empty.
missing=()
for var in PUBLIC_HOSTNAME POSTGRES_PASSWORD; do
  val=$(grep -E "^$var=" .env | cut -d= -f2- || true)
  [[ -z "$val" ]] && missing+=("$var")
done
[[ ${#missing[@]} -eq 0 ]] || err ".env is missing values for: ${missing[*]}. Edit and re-run."

# ─── 6. Up ───────────────────────────────────────────────────────────────
log "Building and starting fri-app stack..."
docker compose -f docker-compose.prod.yml --env-file .env build
docker compose -f docker-compose.prod.yml --env-file .env up -d

log "Waiting for health..."
for i in {1..30}; do
  if docker compose -f docker-compose.prod.yml exec -T postgres pg_isready -U fri -d fri >/dev/null 2>&1; then
    break
  fi
  sleep 2
done

log ""
log "✓ Deploy complete."
log ""
log "Check status:    docker compose -f docker-compose.prod.yml ps"
log "Tail logs:       docker compose -f docker-compose.prod.yml logs -f --tail=100"
log "Trigger sync:    curl -X POST http://localhost:8080/api/sync/all   (only inside the server)"
log ""
log "Public URL:      https://$(grep -E '^PUBLIC_HOSTNAME=' .env | cut -d= -f2-)"
