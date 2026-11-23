package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/meetstream-ai/cli/internal/api"
	"github.com/meetstream-ai/cli/internal/ui"
)

// defaultBotsLimit is how many bots the human list shows; --json without
// paging flags keeps the API's own page (the script contract).
const defaultBotsLimit = 10

// botsPage is what the list needs from one GET /bots answer.
type botsPage struct {
	bots    []any
	next    string
	hasMore bool
	total   int
}

func readBotsPage(v any) botsPage {
	m := object(v)
	p := botsPage{hasMore: m["hasNextPage"] == true, next: plain(m["nextCursor"])}
	p.bots, _ = m["bots"].([]any)
	if s := object(m["summary"]); s != nil && s["partial"] != true {
		if f, ok := s["total_bots"].(float64); ok {
			p.total = int(f)
		}
	}
	if p.next == "" {
		p.hasMore = false
	}
	return p
}

// fetchBots walks the cursor to the page asked for (or every page with all);
// the cursor never leaves this function.
func fetchBots(cmd *cobra.Command, c *api.Client, limit, page int, all bool) (first any, view botsListView, err error) {
	view = botsListView{Pick: -1, Page: page, Limit: limit, All: all, Start: (page-1)*limit + 1}
	cursor := ""
	for p := 1; ; p++ {
		v, err := c.ListBotsPage(cmd.Context(), limit, cursor)
		if err != nil {
			return nil, view, err
		}
		bp := readBotsPage(v)
		if p == 1 {
			first, view.Total = v, bp.total
		}
		if all {
			view.Bots = append(view.Bots, bp.bots...)
		} else if p == page {
			view.Bots, view.HasMore = bp.bots, bp.hasMore
			return v, view, nil
		}
		if !bp.hasMore {
			if all {
				view.Start = 1
				return first, view, nil
			}
			// Past the last page: no bots.
			return map[string]any{"bots": []any{}, "hasNextPage": false, "nextCursor": nil}, view, nil
		}
		cursor = bp.next
	}
}

func newBotsListCmd() *cobra.Command {
	var limit, page int
	var all bool
	cmd := &cobra.Command{Use: "list", Short: shortFor("list"), Args: cobra.NoArgs}
	f := cmd.Flags()
	f.IntVar(&limit, "limit", defaultBotsLimit, "bots per page, 1-50")
	f.IntVar(&page, "page", 1, "page number, newest first")
	f.BoolVar(&all, "all", false, "every bot (one request per 50)")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		c, err := apiClient(cmd)
		if err != nil {
			return err
		}
		paged := f.Changed("limit") || f.Changed("page") || all
		if g.JSON && !paged {
			// Unchanged for scripts: the API's first page, cursor and all.
			v, err := c.ListBots(cmd.Context())
			if err != nil {
				return apiError(err)
			}
			return emit(v, func() {})
		}
		if limit < 1 || limit > 50 {
			return usageErr("--limit must be between 1 and 50", "for more, use --page or --all")
		}
		if page < 1 {
			return usageErr("--page must be 1 or more", "")
		}
		if all {
			limit, page = 50, 1
		}
		v, view, err := fetchBots(cmd, c, limit, page, all)
		if err != nil {
			return apiError(err)
		}
		if g.JSON {
			if all {
				v = map[string]any{"bots": orEmpty(view.Bots), "hasNextPage": false, "nextCursor": nil, "summary": object(v)["summary"]}
			}
			return emit(v, func() {})
		}
		fmt.Fprintln(ui.Out, renderBotsList(view, ui.Width()))
		return nil
	}
	return cmd
}

func orEmpty(xs []any) []any {
	if xs == nil {
		return []any{}
	}
	return xs
}
