// Package agent is the run loop: pick a cadence from the current mode, collect
// (or not), push, apply whatever the server sends back.
package agent

import (
	"context"
	"log/slog"
	"time"

	"github.com/Zyven-Software-House/commander-agent/internal/alerts"
	"github.com/Zyven-Software-House/commander-agent/internal/api"
	"github.com/Zyven-Software-House/commander-agent/internal/collect"
	"github.com/Zyven-Software-House/commander-agent/internal/config"
)

const (
	ModeOff        = "off"
	ModeBackground = "background"
	ModeLive       = "live"

	maxBacklog = 30
)

type Agent struct {
	version   string
	bootID    string
	startedAt time.Time

	cfg   *config.Store
	api   *api.Client
	coll  *collect.Collector
	alert *alerts.Evaluator

	mode    string
	backlog []collect.Sample
	lastErr string
}

func New(version string, static config.Static, cfg *config.Store, cores int) *Agent {
	return &Agent{
		version:   version,
		startedAt: time.Now(),
		cfg:       cfg,
		api:       api.New(static.APIBaseURL, static.Token),
		coll:      collect.New(static),
		alert:     alerts.New(cores),
		mode:      ModeBackground,
	}
}

func (a *Agent) Run(ctx context.Context) {
	a.coll.Prime()
	a.bootID = readBootID()

	// first contact
	a.tick(ctx)

	for {
		d := a.interval()
		t := time.NewTimer(d)
		select {
		case <-ctx.Done():
			t.Stop()
			slog.Info("shutting down", "mode", a.mode)
			return
		case <-t.C:
			a.tick(ctx)
		}
	}
}

func (a *Agent) interval() time.Duration {
	iv := a.cfg.Get().Intervals
	switch a.mode {
	case ModeLive:
		return dur(iv.Live, 3)
	case ModeOff:
		return dur(iv.Heartbeat, 15)
	default:
		return dur(iv.Background, 60)
	}
}

func (a *Agent) tick(ctx context.Context) {
	cfg := a.cfg.Get()
	beat := api.Beat{
		AgentVersion:  a.version,
		BootID:        a.bootID,
		UptimeSec:     int64(time.Since(a.startedAt).Seconds()),
		ConfigVersion: cfg.Version,
		ReportedMode:  a.mode,
		Error:         a.lastErr,
	}

	var ctrl api.Control
	var err error

	if a.mode == ModeOff {
		ctrl, err = a.api.Heartbeat(beat)
	} else {
		s := a.coll.Collect(cfg)
		al := a.alert.Evaluate(s, cfg)

		samples := append(a.backlog, s)
		if len(samples) > maxBacklog {
			samples = samples[len(samples)-maxBacklog:]
		}

		ctrl, err = a.api.Ingest(api.IngestBody{Beat: beat, Samples: samples, Alerts: al})
		if err != nil {
			a.backlog = samples // keep for next round
		} else {
			a.backlog = a.backlog[:0]
		}
	}

	if err != nil {
		a.lastErr = err.Error()
		slog.Warn("push failed", "err", err, "mode", a.mode)
		return
	}
	a.lastErr = ""

	if ctrl.Config != nil {
		ctrl.Config.Version = ctrl.ConfigVersion
		a.cfg.Apply(*ctrl.Config)
		slog.Info("config applied", "version", ctrl.ConfigVersion)
	}
	if ctrl.Mode != "" && ctrl.Mode != a.mode {
		slog.Info("mode change", "from", a.mode, "to", ctrl.Mode)
		a.mode = ctrl.Mode
	}
}

func dur(sec, fallback int) time.Duration {
	if sec <= 0 {
		sec = fallback
	}
	return time.Duration(sec) * time.Second
}

func readBootID() string {
	return collect.BootID()
}
