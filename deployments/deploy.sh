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
log "Configuring firewall (allow 22, 80, 443; deny everything else)..."
ufw --force reset >/dev/null
ufw default deny incoming
ufw default allow outgoing
ufw allow 22/tcp comment 'SSH'
# Port 80 for ACME HTTP-01 challenges + HTTP → HTTPS redirects.
ufw allow 80/tcp comment 'HTTP (ACME challenge + redirect)'
# Port 443 is the real visitor entry. We terminate TLS ourselves
# (Let's Encrypt via Caddy) since CF in the path got throttled by RU ISPs.
ufw allow 443/tcp comment 'HTTPS (Caddy + Lets Encrypt)'
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

# ─── 3.5. Docker daemon config ─────────────────────────────────────────
# Always: a registry mirror. Docker Hub rate-limits unauthenticated pulls
# per source IP, and Timeweb VPSs share IP ranges, so the limit is often
# already spent by a neighbour. mirror.gcr.io (Google's public Docker Hub
# mirror) has its own quota.
#
# Conditionally: the whole IPv6/NAT64 apparatus below only makes sense on
# an IPv6-only host. The original 2026-05 server was on a cheap tier with
# no IPv4, so containers had to speak IPv6 to reach MediaStack/YouTube,
# and the host needed DNS64 to resolve IPv4-only names like github.com.
#
# On a host WITH IPv4 that setup is worse than useless — forcing container
# DNS at NAT64 resolvers adds latency and a third-party dependency for
# name resolution that plain IPv4 handles natively. So we detect first.
if ip -4 route show default 2>/dev/null | grep -q .; then
  HOST_HAS_IPV4=1
  log "Host has an IPv4 default route — skipping IPv6/NAT64 workarounds."
else
  HOST_HAS_IPV4=0
  warn "Host has no IPv4 route — enabling IPv6 + NAT64 fallbacks."
fi

if ! grep -q 'mirror.gcr.io' /etc/docker/daemon.json 2>/dev/null; then
  log "Configuring Docker daemon..."
  mkdir -p /etc/docker
  if [[ "$HOST_HAS_IPV4" -eq 1 ]]; then
    cat > /etc/docker/daemon.json <<'EOF'
{
  "registry-mirrors": ["https://mirror.gcr.io"]
}
EOF
  else
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
  fi
  systemctl restart docker
  sleep 3
fi

# Host-level NAT64 DNS so an IPv6-only host can still reach IPv4-only
# services like github.com (no AAAA record). Trex's public DNS64
# synthesizes AAAA records routed through their NAT64 gateway. Without
# this, even `git clone` fails — but only on an IPv6-only box.
if [[ "$HOST_HAS_IPV4" -eq 0 ]] && ! grep -q 'nat64' /etc/systemd/resolved.conf.d/nat64.conf 2>/dev/null; then
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
