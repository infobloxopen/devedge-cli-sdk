package clikit

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Operation is a decoded long-running-operation status. It is intentionally
// generic: Done and Name are lifted from well-known fields, and Raw carries the
// full payload for callers that need more.
type Operation struct {
	Name string
	Done bool
	Raw  map[string]any
}

// PollOptions tunes [PollOperation].
type PollOptions struct {
	// DoneField is the boolean payload field signaling completion (default
	// "done").
	DoneField string
	// Interval is the delay between polls (default 1s).
	Interval time.Duration
	// Timeout bounds the whole poll (default 5m). Zero means use the default;
	// use a context deadline for finer control.
	Timeout time.Duration
}

// PollOption mutates [PollOptions].
type PollOption func(*PollOptions)

// WithInterval sets the poll interval.
func WithInterval(d time.Duration) PollOption { return func(o *PollOptions) { o.Interval = d } }

// WithTimeout sets the overall poll timeout.
func WithTimeout(d time.Duration) PollOption { return func(o *PollOptions) { o.Timeout = d } }

// WithDoneField overrides the completion field name.
func WithDoneField(name string) PollOption { return func(o *PollOptions) { o.DoneField = name } }

// PollOperation GETs opURL repeatedly until the operation reports done (per the
// configured field) or the timeout/context elapses. It is engine-agnostic: any
// operations endpoint returning a JSON object with a boolean "done" works.
func PollOperation(ctx context.Context, client *http.Client, opURL string, opts ...PollOption) (*Operation, error) {
	o := PollOptions{DoneField: "done", Interval: time.Second, Timeout: 5 * time.Minute}
	for _, fn := range opts {
		fn(&o)
	}
	if client == nil {
		client = http.DefaultClient
	}
	ctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()

	for {
		op, err := getOperation(ctx, client, opURL, o.DoneField)
		if err != nil {
			return nil, err
		}
		if op.Done {
			return op, nil
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("operation %s not done before timeout: %w", opURL, ctx.Err())
		case <-time.After(o.Interval):
		}
	}
}

func getOperation(ctx context.Context, client *http.Client, opURL, doneField string) (*Operation, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, opURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("poll %s: unexpected status %s", opURL, resp.Status)
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("poll %s: decode: %w", opURL, err)
	}
	op := &Operation{Raw: raw}
	if v, ok := raw["name"].(string); ok {
		op.Name = v
	}
	if v, ok := raw[doneField].(bool); ok {
		op.Done = v
	}
	return op, nil
}
