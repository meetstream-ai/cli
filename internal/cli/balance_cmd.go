package cli

import (
	"errors"
	"fmt"

	"github.com/meetstream-ai/cli/internal/api"
	"github.com/meetstream-ai/cli/internal/ui"
	"github.com/spf13/cobra"
)

func newBalanceCmd() *cobra.Command {
	return &cobra.Command{Use: "balance", Short: "Show prepaid balance", GroupID: "meet", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		c, err := apiClient(cmd)
		if err != nil {
			return err
		}
		v, err := c.Balance(cmd.Context())
		var ae *api.Error
		if errors.As(err, &ae) && ae.Status == 404 {
			message := "The balance endpoint is not available for this account yet. See your balance at https://app.meetstream.ai/billing"
			if g.JSON {
				if err := emit(map[string]any{"error": "balance_unavailable", "status": 404, "message": message, "billing_url": "https://app.meetstream.ai/billing"}, func() {}); err != nil {
					return err
				}
				return &ExitError{Code: 3, Message: message, Hint: "https://app.meetstream.ai/billing", Silent: true}
			}
			return &ExitError{Code: 3, Message: "Balance isn't available for this account yet.", Hint: "https://app.meetstream.ai/billing"}
		}
		if err != nil {
			return apiError(err)
		}
		return emit(v, func() { fmt.Fprintln(ui.Out, renderBalance(v)) })
	}}
}
