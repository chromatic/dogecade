package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultPulseTimeDeciseconds is Tasmota's PulseTime unit in tenths of a
// second. The chapter's boards use a 200ms pulse ("PulseTime{n} 2").
const DefaultPulseTimeDeciseconds = 2

// Client is a minimal HTTP client for a Tasmota-flashed relay board,
// speaking its "http://<host>/cm?cmnd=<command>" console interface.
type Client struct {
	baseURL string
	http    *http.Client
}

// NewClient creates a Client for the Tasmota board reachable at baseURL
// (e.g. "http://relay1.lan" or "http://192.168.1.50").
func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: 10 * time.Second},
	}
}

// Pulse fires the given relay channel: it re-sets PulseTime before every
// pulse (per the chapter's advice — Tasmota boards forget PulseTime config
// on reboot) and turns the relay on in a single "Backlog" request, so the
// board can't be left mid-configuration by a dropped connection between two
// separate calls. If pulseTimeDeciseconds is <= 0, DefaultPulseTimeDeciseconds
// is used.
func (c *Client) Pulse(ctx context.Context, relayNumber int, pulseTimeDeciseconds int) error {
	if pulseTimeDeciseconds <= 0 {
		pulseTimeDeciseconds = DefaultPulseTimeDeciseconds
	}
	cmd := fmt.Sprintf("Backlog PulseTime%d %d; Power%d ON", relayNumber, pulseTimeDeciseconds, relayNumber)
	return c.do(ctx, cmd, nil)
}

// StatusResult holds the raw JSON fields of a Tasmota "Status" response,
// used only to confirm the board is alive and responding sensibly.
type StatusResult struct {
	Raw map[string]json.RawMessage
}

// Status queries the board's health via the Tasmota "Status" command.
// Returns an error if the board is unreachable or returns something that
// doesn't look like a Tasmota status response.
func (c *Client) Status(ctx context.Context) (StatusResult, error) {
	var raw map[string]json.RawMessage
	if err := c.do(ctx, "Status", &raw); err != nil {
		return StatusResult{}, err
	}
	return StatusResult{Raw: raw}, nil
}

// do sends a single Tasmota console command and, if out is non-nil,
// unmarshals the JSON response into it. If out is nil, the response is
// still parsed and checked for Tasmota's "Command":"Unknown" error marker.
func (c *Client) do(ctx context.Context, cmnd string, out interface{}) error {
	reqURL := c.baseURL + "/cm?cmnd=" + url.QueryEscape(cmnd)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return fmt.Errorf("failed to build tasmota request: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("tasmota request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read tasmota response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("tasmota returned HTTP %d: %s", resp.StatusCode, string(body))
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return fmt.Errorf("failed to parse tasmota response: %w", err)
	}

	if cmdVal, ok := raw["Command"]; ok {
		var s string
		if err := json.Unmarshal(cmdVal, &s); err == nil && s == "Unknown" {
			return fmt.Errorf("tasmota reported unknown command %q", cmnd)
		}
	}

	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("failed to parse tasmota response: %w", err)
		}
	}

	return nil
}
