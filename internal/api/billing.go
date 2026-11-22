package api

import "context"

func (c *Client) Balance(ctx context.Context) (any, error) {
	return c.getJSON(ctx, "/billing/balance")
}
