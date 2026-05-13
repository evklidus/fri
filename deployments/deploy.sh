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

# ─── 0.5. Swap (so `go build` doesn't OOM on a 2GB VPS) ──────────────────
# Linking the Gin / ugorji codec dependency peaks above 1.5GB during
# compilation. On a 2GB VPS without swap that triggers the OOM killer and
# the build fails with `signal: killed`. A 2GB swapfile soaks the peak.
# It's only touched during builds — no runtime cost.
if ! swapon --show | grep -q '/swapfile'; then
  log "Creating 2GB swapfile so Go builds don't OOM on a 2GB VPS..."
  fallocate -l 2G /swapfile
  chmod 600 /swapfile
  mkswap /swapfile >/dev/null
  swapon /swapfile
  grep -q '/swapfile' /etc/fstab || echo '/swapfile none swap sw 0 0' >> /etc/fstab
fi

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
log "Configuring firewall (allow 22, 80; deny everything else)..."
ufw --force reset >/dev/null
ufw default deny incoming
ufw default allow outgoing
ufw allow 22/tcp comment 'SSH'
# Port 80 only — Cloudflare terminates HTTPS at the edge and talks HTTP
# to the origin, so we don't need 443 open here.
ufw allow 80/tcp comment 'HTTP (behind Cloudflare proxy)'
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

# ─── 3.5. Docker daemon: registry mirror + IPv6 ────────────────────────
# Two reasons we replace daemon.json:
#   1. Docker Hub rate-limits unauthenticated pulls per IP. Timeweb VPSs
#      share IP ranges, so the limit is often already exhausted. We
#      configure mirror.gcr.io (Google's public Docker Hub mirror) which
#      has a separate quota.
#   2. On IPv6-only VPSs (cheap Timeweb tier without a paid IPv4) we need
#      Docker containers themselves to have IPv6 — otherwise outbound
#      requests to MediaStack / YouTube APIs fail with "no route". The
#      `ipv6: true` + `ip6tables: true` + `fixed-cidr-v6` triple does
#      this; the `dns` block points containers at public DNS64 resolvers
#      so IPv4-only hostnames still resolve (via NAT64 synthesis).
if ! grep -q 'mirror.gcr.io' /etc/docker/daemon.json 2>/dev/null; then
  log "Configuring Docker daemon (registry mirror + IPv6)..."
  mkdir -p /etc/docker
  cat > /etc/docker/daemon.json <<'EOF'
{
  "registry-mirrors": ["https://mirror.gcr.io"],
  "ipv6": true,
  "fixed-cidr-v6": "fd00:dead:beef::/48",
  "ip6tables": true,
  "experimental": true,
  "dns": ["2001:4860:4860::6464", "2001:4860:4860::64", "2a01:4f9:c010:3f02::1"]
}
EOF
  systemctl restart docker
  sleep 3
fi

# Host-level NAT64 DNS so the host itself can reach IPv4-only services
# like github.com (which has no AAAA record). Trex's free public DNS64
# synthesizes AAAA for IPv4-only hosts, routed through their NAT64
# gateway. Without this, even `git clone` fails on an IPv6-only VPS.
if ! grep -q 'nat64' /etc/systemd/resolved.conf.d/nat64.conf 2>/dev/null; then
  log "Configuring host NAT64 DNS (so the host can clone github.com)..."
  mkdir -p /etc/systemd/resolved.conf.d
  cat > /etc/systemd/resolved.conf.d/nat64.conf <<'EOF'
[Resolve]
DNS=2a01:4f9:c010:3f02::1 2a00:1098:2b::1 2a00:1098:2c::1
DNSOverTLS=opportunistic
EOF
  systemctl restart systemd-resolved
  resolvectl flush-caches
  sleep 2
fi

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
  warn "  - PUBLIC_HOSTNAME (your domain, e.g. footballreputation.ru)"
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
