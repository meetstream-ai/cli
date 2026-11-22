// Package api is a small client for api.meetstream.ai (`Authorization: Token <key>`).
// M2 needs only key validation; M3 adds the bot, transcript and MIA calls.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	BaseURL   string
	Key       string
	UserAgent string
	HTTP      *http.Client
}

// Error is a non-2xx answer from the API. Its message never contains the key.
// 507 is not an Error: an idempotent replay is a success.
type Error struct {
	Status     int
	Body       string
	Method     string
	Path       string
	RetryAfter *int
}

// Detail is the API's own message: message, then detail, then error, then the body.
func (e *Error) Detail() string {
	raw := strings.TrimSpace(e.Body)
	if raw == "" {
		return ""
	}
	var v any
	if json.Unmarshal([]byte(raw), &v) != nil {
		return clip(raw)
	}
	if m, ok := v.(map[string]any); ok {
		for _, k := range []string{"message", "detail", "error"} {
			if s, ok := m[k].(string); ok && s != "" {
				return clip(s)
			}
		}
		b, err := json.Marshal(m)
		if err != nil {
			return clip(raw)
		}
		return clip(string(b))
	}
	if s, ok := v.(string); ok {
		return clip(s)
	}
	return clip(raw)
}

func clip(s string) string {
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}

func (e *Error) Error() string {
	msg := e.Detail()
	if e.RetryAfter != nil {
		msg += fmt.Sprintf(" (retry after %d s)", *e.RetryAfter)
	}
	if e.Method != "" {
		return fmt.Sprintf("%s %s → HTTP %d: %s", e.Method, e.Path, e.Status, msg)
	}
	return fmt.Sprintf("MeetStream API returned %d: %s", e.Status, msg)
}

func New(baseURL, key, version string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"), Key: key,
		UserAgent: "meetstream-cli-go/" + version,
		HTTP:      &http.Client{Timeout: 30 * time.Second},
	}
}

// Do sends a request and decodes a JSON answer into out (when out is non-nil).
func (c *Client) Do(ctx context.Context, method, path string, body io.Reader, out any) error {
	_, err := c.DoHeader(ctx, method, path, body, nil, out)
	return err
}

// DoHeader is Do plus extra headers. It returns the status. 507 is success.
func (c *Client) DoHeader(ctx context.Context, method, path string, body io.Reader, headers map[string]string, out any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Token "+c.Key)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.UserAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return res.StatusCode, err
	}
	if res.StatusCode/100 != 2 && res.StatusCode != http.StatusInsufficientStorage {
		return res.StatusCode, &Error{
			Status: res.StatusCode, Body: string(raw), Method: method, Path: path,
			RetryAfter: retryAfter(res),
		}
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return res.StatusCode, err
		}
	}
	return res.StatusCode, nil
}

func retryAfter(res *http.Response) *int {
	v := strings.TrimSpace(res.Header.Get("Retry-After"))
	if v == "" {
		return nil
	}
	if n, err := strconv.ParseFloat(v, 64); err == nil && !math.IsNaN(n) {
		s := int(math.Ceil(n))
		if s < 0 {
			s = 0
		}
		return &s
	}
	if t, err := http.ParseTime(v); err == nil {
		s := int(math.Ceil(time.Until(t).Seconds()))
		if s < 0 {
			s = 0
		}
		return &s
	}
	return nil
}

func (c *Client) getJSON(ctx context.Context, path string) (any, error) {
	var out any
	err := c.Do(ctx, http.MethodGet, path, nil, &out)
	return out, err
}

func (c *Client) doJSON(ctx context.Context, method, path string, body any, headers map[string]string) (int, any, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		r = bytes.NewReader(b)
	}
	var out any
	status, err := c.DoHeader(ctx, method, path, r, headers, &out)
	return status, out, err
}

// BotCount validates the key the way the JS CLI's `auth status` does: list bots.
// It returns the number of bots on the first page and whether there are more.
func (c *Client) BotCount(ctx context.Context) (int, bool, error) {
	var page struct {
		Bots        []json.RawMessage `json:"bots"`
		HasNextPage bool              `json:"hasNextPage"`
	}
	if err := c.Do(ctx, http.MethodGet, "/bots", nil, &page); err != nil {
		return 0, false, err
	}
	return len(page.Bots), page.HasNextPage, nil
}
