package cli

import (
	"errors"
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
			return &ExitError{Code: 3, Message: message, Hint: "https://app.meetstream.ai/billing"}
		}
		if err != nil {
			return apiError(err)
		}
		return emit(v, func() {
			m := object(v)
			balance := "no prepaid balance (billed by invoice)"
			if m["balance_usd"] != nil {
				currency := text(m["currency"])
				if currency == "" {
					currency = "USD"
				}
				balance = text(m["balance_usd"]) + " " + currency
			}
			ui.Box("Balance", [][2]string{{"customer_type", text(m["customer_type"])}, {"balance", balance}})
			ui.Note("Top up: https://app.meetstream.ai/billing")
		})
	}}
}
