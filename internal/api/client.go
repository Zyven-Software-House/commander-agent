// Package api is the agent's client for the Commander monitoring endpoints.
package api

import (
	"bytes"
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
		base:  strings.TrimRight(base, "/"),
		token: token,
		http:  &http.Client{Timeout: 15 * time.Second},
	}
}

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
	return c.post("/agent/heartbeat", b)
}

func (c *Client) Ingest(body IngestBody) (Control, error) {
	return c.post("/agent/ingest", body)
}

func (c *Client) post(path string, payload any) (Control, error) {
	var ctrl Control
	raw, err := json.Marshal(payload)
	if err != nil {
		return ctrl, err
	}

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 2 * time.Second)
		}
		req, _ := http.NewRequest(http.MethodPost, c.base+path, bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")

		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()

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
