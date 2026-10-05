// Package api is the agent's client for the Commander monitoring endpoints.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Zyven-Software-House/commander-agent/internal/alerts"
	"github.com/Zyven-Software-House/commander-agent/internal/collect"
	"github.com/Zyven-Software-House/commander-agent/internal/config"
)

type Client struct {
	base  string
	token string
	http  *http.Client
}

func New(base, token string) *Client {
	return &Client{
		base: strings.TrimRight(base, "/"),
		// No Client-level Timeout: each call sets its own via context (CallOptions), since live and
		// background/heartbeat calls need very different budgets — see CallOptions.
		token: token,
		http:  &http.Client{},
	}
}

// CallOptions controls one call's timeout and retry budget. Live ingest must never sit on a stalled
// request: a stale live sample isn't worth retrying, and the whole point of the short timeout is to
// free the agent for its next 2s tick rather than block it. Background/heartbeat has a much looser
// cadence (60s/15s), so it can afford to retry through a transient server hiccup.
type CallOptions struct {
	Timeout time.Duration
	Retries int // total attempts, including the first; 1 means no retry
}

// LiveCallOptions is what Ingest should use while the agent is in live mode.
var LiveCallOptions = CallOptions{Timeout: 5 * time.Second, Retries: 1}

// BackgroundCallOptions is what Ingest/Heartbeat should use outside live mode.
var BackgroundCallOptions = CallOptions{Timeout: 15 * time.Second, Retries: 3}

// Beat is the common part of both requests.
type Beat struct {
	AgentVersion  string `json:"agentVersion"`
	BootID        string `json:"bootId"`
	UptimeSec     int64  `json:"uptimeSec"`
	ConfigVersion int    `json:"configVersion"`
	ReportedMode  string `json:"reportedMode"`
	Error         string `json:"error,omitempty"`
}

type IngestBody struct {
	Beat
	Samples []collect.Sample `json:"samples"`
	Alerts  []alerts.Alert   `json:"alerts,omitempty"`
}

// Control is what the server sends back on every call.
type Control struct {
	Mode          string        `json:"mode"`
	ModeExpiresAt *string       `json:"modeExpiresAt"`
	ConfigVersion int           `json:"configVersion"`
	Config        *config.Block `json:"config"`
}

func (c *Client) Heartbeat(b Beat) (Control, error) {
	return c.post("/agent/heartbeat", b, BackgroundCallOptions)
}

// Ingest's opts should be LiveCallOptions while the caller is in live mode, BackgroundCallOptions
// otherwise — the agent package decides based on its own current mode, not this package.
func (c *Client) Ingest(body IngestBody, opts CallOptions) (Control, error) {
	return c.post("/agent/ingest", body, opts)
}

func (c *Client) post(path string, payload any, opts CallOptions) (Control, error) {
	var ctrl Control
	raw, err := json.Marshal(payload)
	if err != nil {
		return ctrl, err
	}

	var lastErr error
	for attempt := 0; attempt < opts.Retries; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 2 * time.Second)
		}

		ctx, cancel := context.WithTimeout(context.Background(), opts.Timeout)
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")

		resp, err := c.http.Do(req)
		if err != nil {
			cancel()
			lastErr = err
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		cancel()

		if resp.StatusCode == 401 || resp.StatusCode == 403 {
			return ctrl, fmt.Errorf("auth rejected (%d) — token invalid", resp.StatusCode)
		}
		if resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("server %d", resp.StatusCode)
			continue
		}
		if resp.StatusCode != 200 {
			return ctrl, fmt.Errorf("%s: %d %s", path, resp.StatusCode, string(body))
		}
		if err := json.Unmarshal(body, &ctrl); err != nil {
			return ctrl, fmt.Errorf("bad control response: %w", err)
		}
		return ctrl, nil
	}
	return ctrl, lastErr
}
