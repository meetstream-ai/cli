package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// CalendarOAuthStart is the answer to POST /calendar/oauth/start.
type CalendarOAuthStart struct {
	AuthURL   string `json:"auth_url"`
	SessionID string `json:"session_id"`
	ExpiresAt int64  `json:"expires_at"` // Unix seconds
}

// CalendarOAuthState is the answer to GET /calendar/oauth/status.
// Status is pending, connected or failed; AccountEmail is set once connected.
type CalendarOAuthState struct {
	Status       string `json:"status"`
	Provider     string `json:"provider"`
	AccountEmail string `json:"account_email"`
}

// StartCalendarOAuth starts a hosted calendar sign-in. provider is google or
// microsoft, joinRule is accepted or all; defaultBotConfig may be nil.
// A server without hosted sign-in answers 404 (an *Error).
func (c *Client) StartCalendarOAuth(ctx context.Context, provider, joinRule string, defaultBotConfig map[string]any) (*CalendarOAuthStart, error) {
	body := map[string]any{"provider": provider, "join_rule": joinRule}
	if defaultBotConfig != nil {
		body["default_bot_config"] = defaultBotConfig
	}
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	var out CalendarOAuthStart
	if err := c.Do(ctx, http.MethodPost, "/calendar/oauth/start", bytes.NewReader(b), &out); err != nil {
		return nil, err
	}
	u, err := url.Parse(out.AuthURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || out.SessionID == "" {
		return nil, fmt.Errorf("unexpected answer from POST /calendar/oauth/start")
	}
	return &out, nil
}

// CalendarOAuthStatus reads the state of a hosted calendar sign-in.
func (c *Client) CalendarOAuthStatus(ctx context.Context, sessionID string) (*CalendarOAuthState, error) {
	var out CalendarOAuthState
	if err := c.Do(ctx, http.MethodGet, "/calendar/oauth/status?"+url.Values{"session_id": {sessionID}}.Encode(), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
