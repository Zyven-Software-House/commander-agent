// commander-agent — a lightweight host metrics agent for Commander.
//
// It reads /proc (+ the Docker socket) on a cadence set by the server, pushes
// samples to the Commander API, and evaluates alert thresholds locally. Three
// modes (off / background / live) keep it cheap when nobody is watching.
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"

	"github.com/Zyven-Software-House/commander-agent/internal/agent"
	"github.com/Zyven-Software-House/commander-agent/internal/config"
)

var version = "dev" // set by -ldflags at build time

func main() {
	var (
		apiURL     = flag.String("api", env("AGENT_API_URL", ""), "Commander API base URL (…/api)")
		token      = flag.String("token", env("AGENT_TOKEN", ""), "agent token (cma_…)")
		cfgPath    = flag.String("config", env("AGENT_CONFIG", "/etc/commander-agent/config.json"), "path to the persisted server config")
		dockerSock = flag.String("docker", env("AGENT_DOCKER_SOCK", "/var/run/docker.sock"), "docker socket (empty disables container metrics)")
		mounts     = flag.String("mounts", env("AGENT_MOUNTS", "/,/var/lib/docker"), "comma-separated filesystems to watch")
		showVer    = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()

	if *showVer {
		println("commander-agent", version)
		return
	}

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	if *apiURL == "" || *token == "" {
		slog.Error("missing --api / --token (or AGENT_API_URL / AGENT_TOKEN)")
		os.Exit(2)
	}
	if _, err := os.Stat(*dockerSock); err != nil {
		slog.Info("docker socket not found — container metrics disabled", "path", *dockerSock)
		*dockerSock = ""
	}

	static := config.Static{
		APIBaseURL: *apiURL,
		Token:      *token,
		ConfigPath: *cfgPath,
		DockerSock: *dockerSock,
		RootMounts: splitClean(*mounts),
	}

	store := config.Load(*cfgPath)
	a := agent.New(version, static, store, runtime.NumCPU())

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	slog.Info("commander-agent starting", "version", version, "api", *apiURL, "cores", runtime.NumCPU())
	a.Run(ctx)
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func splitClean(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
