// vendored from github.com/hzeng10/log-analysis cli/internal/client @v0.1.1

// Package client is a thin HTTP client for the VictoriaLogs query API.
package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Client talks to a VictoriaLogs instance.
type Client struct {
	base string
	hc   *http.Client
}

// New returns a Client for the given base URL (e.g. http://localhost:9428).
func New(addr string, timeout time.Duration) *Client {
	return &Client{
		base: strings.TrimRight(addr, "/"),
		hc:   &http.Client{Timeout: timeout},
	}
}

// Log is a single decoded log record (arbitrary fields).
type Log = map[string]any

// Query runs a LogsQL query and returns up to `limit` records. With the default
// sort (_time desc) it relies on the `limit` arg returning the most-recent N
// (efficient); any other sort is appended as a `| sort` pipe.
func (c *Client) Query(ctx context.Context, query, sort string, limit, offset int) ([]Log, error) {
	q := query
	field, dir := parseSort(sort)
	if !(field == "_time" && dir == "desc") && !strings.Contains(query, "|") {
		q = fmt.Sprintf("%s | sort by (%s) %s", query, field, dir)
	}

	form := url.Values{}
	form.Set("query", q)
	if limit > 0 {
		form.Set("limit", strconv.Itoa(limit))
	}
	if offset > 0 {
		form.Set("offset", strconv.Itoa(offset))
	}

	resp, err := c.post(ctx, "/select/logsql/query", form)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var out []Log
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1024*1024), 64*1024*1024) // tolerate very long lines
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var m Log
		if err := json.Unmarshal(line, &m); err != nil {
			continue // skip malformed lines rather than abort the whole query
		}
		out = append(out, m)
	}
	return out, sc.Err()
}

// Tail streams live logs matching the query, invoking fn for each record until
// the context is cancelled or the stream ends.
func (c *Client) Tail(ctx context.Context, query string, fn func(Log)) error {
	form := url.Values{}
	form.Set("query", query)
	resp, err := c.post(ctx, "/select/logsql/tail", form)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1024*1024), 64*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var m Log
		if err := json.Unmarshal(line, &m); err != nil {
			continue
		}
		fn(m)
	}
	return sc.Err()
}

// Stats runs a query that must contain a `| stats` pipe and returns the raw
// (Prometheus-shaped) JSON response.
func (c *Client) Stats(ctx context.Context, query string) (json.RawMessage, error) {
	form := url.Values{}
	form.Set("query", query)
	return c.postRaw(ctx, "/select/logsql/stats_query", form)
}

// FieldNames returns the raw JSON list of field names matching the query.
func (c *Client) FieldNames(ctx context.Context, query string) (json.RawMessage, error) {
	form := url.Values{}
	form.Set("query", query)
	return c.postRaw(ctx, "/select/logsql/field_names", form)
}

// FieldValues returns the raw JSON list of values for a field matching the query.
func (c *Client) FieldValues(ctx context.Context, field, query string, limit int) (json.RawMessage, error) {
	form := url.Values{}
	form.Set("query", query)
	form.Set("field", field)
	if limit > 0 {
		form.Set("limit", strconv.Itoa(limit))
	}
	return c.postRaw(ctx, "/select/logsql/field_values", form)
}

func (c *Client) post(ctx context.Context, path string, form url.Values) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request to %s failed: %w", c.base+path, err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		return nil, fmt.Errorf("%s returned %s: %s", path, resp.Status, strings.TrimSpace(string(body)))
	}
	return resp, nil
}

func (c *Client) postRaw(ctx context.Context, path string, form url.Values) (json.RawMessage, error) {
	resp, err := c.post(ctx, path, form)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func parseSort(sort string) (field, dir string) {
	field, dir = "_time", "desc"
	parts := strings.Fields(sort)
	if len(parts) >= 1 && parts[0] != "" {
		field = parts[0]
	}
	if len(parts) >= 2 {
		dir = strings.ToLower(parts[1])
	}
	if dir != "asc" && dir != "desc" {
		dir = "desc"
	}
	return field, dir
}
