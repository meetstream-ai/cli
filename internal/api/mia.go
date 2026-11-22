package api

import (
	"context"
	"net/http"
	"net/url"
)

func (c *Client) MiaList(ctx context.Context) (any, error) {
	return c.getJSON(ctx, "/mia")
}

func (c *Client) MiaGet(ctx context.Context, id string) (any, error) {
	return c.getJSON(ctx, "/mia?"+url.Values{"agent_config_id": {id}}.Encode())
}

func (c *Client) MiaCreate(ctx context.Context, config any) (any, error) {
	_, data, err := c.doJSON(ctx, http.MethodPost, "/mia", config, nil)
	return data, err
}

func (c *Client) MiaUpdate(ctx context.Context, config any) (any, error) {
	_, data, err := c.doJSON(ctx, http.MethodPut, "/mia", config, nil)
	return data, err
}

func (c *Client) MiaDelete(ctx context.Context, id string) (any, error) {
	var out any
	err := c.Do(ctx, http.MethodDelete, "/mia?"+url.Values{"agent_config_id": {id}}.Encode(), nil, &out)
	return out, err
}

func (c *Client) MiaCheckLocalAgent(ctx context.Context, id string) (any, error) {
	q := url.Values{"agent_config_id": {id}, "check": {"local_agent"}}
	return c.getJSON(ctx, "/mia?"+q.Encode())
}
