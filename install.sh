#!/usr/bin/env bash
# Commander monitoring agent installer.
#
#   curl -fsSL https://raw.githubusercontent.com/Zyven-Software-House/commander-agent/main/install.sh \
#     | sudo AGENT_API_URL=https://…/api AGENT_TOKEN=cma_… bash
#
# Re-run any time to upgrade the binary (config + token are kept).
set -euo pipefail

REPO="Zyven-Software-House/commander-agent"
BIN=/usr/local/bin/commander-agent
ETC=/etc/commander-agent
UNIT=/etc/systemd/system/commander-agent.service
VERSION="${AGENT_VERSION:-latest}"

[ "$(id -u)" = 0 ] || { echo "run as root (sudo)"; exit 1; }
: "${AGENT_API_URL:?set AGENT_API_URL}"
: "${AGENT_TOKEN:?set AGENT_TOKEN}"

case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) echo "unsupported arch: $(uname -m)"; exit 1 ;;
esac

if [ "$VERSION" = latest ]; then
  URL="https://github.com/${REPO}/releases/latest/download/commander-agent-linux-${ARCH}"
else
  URL="https://github.com/${REPO}/releases/download/${VERSION}/commander-agent-linux-${ARCH}"
fi

echo "→ downloading commander-agent (${ARCH}, ${VERSION})"
tmp="$(mktemp)"
curl -fsSL "$URL" -o "$tmp"
chmod +x "$tmp"
install -m 0755 "$tmp" "$BIN"
rm -f "$tmp"

mkdir -p "$ETC"
cat > "${ETC}/agent.env" <<EOF
AGENT_API_URL=${AGENT_API_URL}
AGENT_TOKEN=${AGENT_TOKEN}
EOF
chmod 600 "${ETC}/agent.env"

cp_unit() {
  cat > "$UNIT" <<'EOF'
[Unit]
Description=Commander monitoring agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
EnvironmentFile=/etc/commander-agent/agent.env
ExecStart=/usr/local/bin/commander-agent
Restart=always
RestartSec=10
ProtectSystem=strict
ReadWritePaths=/etc/commander-agent
ProtectHome=yes
NoNewPrivileges=yes
SupplementaryGroups=docker
MemoryMax=64M
CPUQuota=15%

[Install]
WantedBy=multi-user.target
EOF
}
cp_unit

systemctl daemon-reload
systemctl enable --now commander-agent

echo "✓ installed. status:  systemctl status commander-agent"
echo "  logs:            journalctl -u commander-agent -f"
"$BIN" -version
