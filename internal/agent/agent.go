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

	// A ticker, not a timer re-created after every tick: a timer rearmed only once tick() returns
	// means a slow tick (e.g. a stalled HTTP call) pushes every following one back by exactly that
	// much, collapsing live mode's cadence to "however long the last call took". A ticker keeps firing
	// on its own fixed schedule underneath; since its channel only buffers one pending tick and drops
	// the rest while nobody's reading it (see time.Ticker docs), a slow tick can delay the NEXT one but
	// never queues up a backlog of them — effectively "skip ticks while one is still in flight" for
	// free, with no goroutines and no risk of two ticks mutating agent state at once.
	//
	// Reset() is only called when the interval itself changed (a mode switch), not after every tick:
	// resetting unconditionally would re-add tick()'s own duration on top of the interval every single
	// time (a normally-fast ~0.3s tick would make a "2s" cadence run at ~2.3s), defeating the point of
	// using a fixed-schedule ticker in the first place.
	current := a.interval()
	ticker := time.NewTicker(current)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("shutting down", "mode", a.mode)
			return
		case <-ticker.C:
			a.tick(ctx)

			if next := a.interval(); next != current {
				current = next
				ticker.Reset(current)
			}
		}
	}
}

func (a *Agent) interval() time.Duration {
	iv := a.cfg.Get().Intervals
	switch a.mode {
	case ModeLive:
		return dur(iv.Live, 2)
	case ModeOff:
		return dur(iv.Heartbeat, 15)
	default:
		return dur(iv.Background, 60)
	}
}

func (a *Agent) tick(ctx context.Context) {
	started := time.Now()
	slog.Info("tick started", "mode", a.mode)
	defer func() {
		slog.Info("tick finished", "mode", a.mode, "duration", time.Since(started))
	}()

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

		// Live can't afford to retry a stalled request (see api.LiveCallOptions's docs): a sample
		// that's late is just stale, and the point of the short timeout is to free the agent for its
		// next tick, not to keep hammering a slow server. Background/heartbeat still retries.
		opts := api.BackgroundCallOptions
		if a.mode == ModeLive {
			opts = api.LiveCallOptions
		}

		ctrl, err = a.api.Ingest(api.IngestBody{Beat: beat, Samples: samples, Alerts: al}, opts)
		switch {
		case err != nil && a.mode == ModeLive:
			// Discard, don't backlog: a live sample is stale within ~2s, so carrying a failed one
			// into the next tick (and bundling it with a fresh sample) buys nothing.
			a.backlog = a.backlog[:0]
		case err != nil:
			a.backlog = samples // keep for next round
		default:
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
