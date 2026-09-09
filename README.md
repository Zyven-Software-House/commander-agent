# commander-agent

A lightweight host-metrics agent for [Commander](../commander-api). One static Go
binary (~10 MB, ~15 MB RSS), no runtime deps.

## What it does

Reads `/proc` (+ the Docker socket) and pushes samples to
`POST {api}/agent/ingest`. The server replies with the mode + config; the agent
persists the config to `/etc/commander-agent/config.json`.

**Three modes** (server-driven):

| mode | cadence | collects |
|---|---|---|
| `off` | ~15 s heartbeat | nothing |
| `background` | ~60 s | slow metrics, feeds alerts + the 1-min rollup |
| `live` | ~3 s, streamed | everything (per-core CPU, docker stats) — only while someone watches the server in the panel |

Alert thresholds are evaluated locally and sent with each push, so alerts work
even when realtime is off.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/Zyven-Software-House/commander-agent/main/install.sh \
  | sudo AGENT_API_URL=https://ytox3asfh06f72kv-d.commander.zyven.io/api AGENT_TOKEN=cma_… bash
```

Get the token from the panel (server → Monitoramento → gerar token) or
`php artisan agent:issue-token` on the API.

Re-run the same command to upgrade.

## Run manually / dev

```sh
go build -o commander-agent .
AGENT_API_URL=… AGENT_TOKEN=… ./commander-agent
```

Flags: `-api`, `-token`, `-config`, `-docker`, `-mounts`, `-version`.

## Uninstall

```sh
sudo systemctl disable --now commander-agent
sudo rm /usr/local/bin/commander-agent /etc/systemd/system/commander-agent.service
sudo rm -rf /etc/commander-agent
sudo systemctl daemon-reload
```

## Releases

Tag `vX.Y.Z` → CI builds `commander-agent-linux-{amd64,arm64}` + `SHA256SUMS`.
