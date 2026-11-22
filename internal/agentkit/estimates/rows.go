package estimates

import (
	"fmt"
	"strings"

	"github.com/meetstream-ai/cli/internal/agentkit/agents"
	"github.com/meetstream-ai/cli/internal/agentkit/models"
	"github.com/meetstream-ai/cli/internal/agentkit/pricing"
	"github.com/meetstream-ai/cli/internal/agentkit/usage"
)

// HourCost is an hour at API rates and where the number comes from: the CLI's own
// price wins, then the dated table. Nil when the turn is nil or no price is known.
func HourCost(agent agents.Spec, model string, turn *usage.Usage) (*float64, string) {
	if turn == nil {
		return nil, ""
	}
	hour := usage.ProjectHour(*turn, nil)
	if hour.CostUSD != nil {
		return hour.CostUSD, agent.Label + "'s own estimate"
	}
	price := pricing.PriceFor(model, pricing.Load())
	if price == nil {
		return nil, ""
	}
	cost := pricing.HourCost(hour, *turn, *price)
	return &cost, fmt.Sprintf("%s list prices, %s", price.Vendor, price.AsOf)
}

// ChipState is how a feature chip should look.
type ChipState int

const (
	ChipUnknown ChipState = iota // "vision?" (yellow in the kit)
	ChipOn                       // "tools" (green)
	ChipOff                      // "no-tools" (dim)
)

// Chip is one feature chip on a row.
type Chip struct {
	Label string // "tools", "no-tools" or "tools?"
	State ChipState
}

// Row is a picker row as plain data; the caller styles and clips it.
type Row struct {
	Model         string   // the model id, or "The CLI's default"
	Bar           string   // usage bar, up to 10 cells (empty when not measured)
	TokensPerHour int64    // 0 when not measured
	Usage         string   // "223k/h", "not measured" or "usage n/a"
	Kind          string   // pricing.Plan, Free, API or ""
	Cost          *float64 // API-rate cost of the hour; nil when unknown or free
	CostText      string   // "≈$0.45", "—" or "free"
	Chips         []Chip
	FirstWords    string // "~5 s cold" or "—"
}

// Plain is the row without colour or clipping, in the kit's order:
// "[bar      ] 223k/h ≈$0.45 plan  talk tools vision local  ~5 s cold".
func (r Row) Plain() string {
	usageText := r.Usage + " " + r.CostText
	if r.Kind != "" && r.Kind != pricing.Free {
		usageText += " " + r.Kind
	}
	labels := make([]string, len(r.Chips))
	for i, c := range r.Chips {
		labels[i] = c.Label
	}
	bar := r.Bar + strings.Repeat(" ", max(0, 10-len([]rune(r.Bar))))
	return fmt.Sprintf("%s  [%s] %s  %s  %s", r.Model, bar, usageText, strings.Join(labels, " "), r.FirstWords)
}

func firstWords(e *Estimate, format string) string {
	if e == nil || e.FirstOutputMS == nil {
		return "—"
	}
	return fmt.Sprintf(format, *e.FirstOutputMS/1000)
}

func turnOf(e *Estimate) *usage.Usage {
	if e == nil {
		return nil
	}
	u := e.Usage
	return &u
}

// RowFor is a picker row: a usage bar scaled to largest (tokens/h of the biggest
// row), tokens/h, the hour at API rates or "—", the plan tag, feature chips and
// the cold first-words time.
func RowFor(agent agents.Spec, model models.Choice, e *Estimate, largest int64) Row {
	r := Row{Model: model.ID, Kind: pricing.PlanKind(agent.Name, model.ID, pricing.PlanOptions{}), FirstWords: firstWords(e, "~%.0f s cold")}
	if r.Model == "" {
		r.Model = "The CLI's default"
	}
	switch {
	case e != nil:
		hour := usage.ProjectHour(e.Usage, nil)
		r.TokensPerHour = hour.Total()
		r.Bar = usage.Bar(float64(hour.Total()), float64(largest), 10)
		r.Usage = usage.HumanTokens(float64(hour.Total())) + "/h"
	case usage.ReportsUsage(agent.Name):
		r.Usage = "not measured"
	default:
		r.Usage = "usage n/a"
	}
	if r.Kind == pricing.Free {
		r.CostText = "free"
	} else if cost, _ := HourCost(agent, model.ID, turnOf(e)); cost != nil {
		r.Cost, r.CostText = cost, "≈"+pricing.Money(*cost)
	} else {
		r.CostText = "—"
	}
	for _, f := range models.Features(agent, model) {
		switch {
		case f.Available == nil:
			r.Chips = append(r.Chips, Chip{f.Name + "?", ChipUnknown})
		case *f.Available:
			r.Chips = append(r.Chips, Chip{f.Name, ChipOn})
		default:
			r.Chips = append(r.Chips, Chip{"no-" + f.Name, ChipOff})
		}
	}
	return r
}

func billingText(agent agents.Spec, model string, e *Estimate) []string {
	kind := pricing.PlanKind(agent.Name, model, pricing.PlanOptions{})
	if kind == pricing.Free {
		return []string{"free model"}
	}
	var parts []string
	if cost, source := HourCost(agent, model, turnOf(e)); cost != nil {
		parts = append(parts, fmt.Sprintf("≈ %s/h at API rates (estimate; %s)", pricing.Money(*cost), source))
	} else if e != nil {
		parts = append(parts, "API price unknown")
	}
	switch kind {
	case pricing.Plan:
		parts = append(parts, "on your plan: no per-token bill")
	case pricing.API:
		parts = append(parts, "billed per token (API key)")
	}
	return parts
}

var featureNames = map[string]string{"talk": "talk", "tools": "meeting tools", "vision": "vision", "local": "local actions"}

// DetailText explains one row in plain text (the kit's detail line): tokens/h,
// cost and its source, the plan, first words, where the estimate came from, and
// the features.
func DetailText(agent agents.Spec, model models.Choice, e *Estimate) string {
	var parts []string
	reports := usage.ReportsUsage(agent.Name)
	switch {
	case e != nil:
		hour := usage.ProjectHour(e.Usage, nil)
		parts = append(parts, "≈ "+usage.HumanTokens(float64(hour.Total()))+" tokens/h")
		parts = append(parts, billingText(agent, model.ID, e)...)
		parts = append(parts, "first words "+firstWords(e, "~%.0f s (cold)"))
		parts = append(parts, e.Source)
	case reports:
		parts = append(parts, "not measured yet")
	default:
		parts = append(parts, "usage not available: "+agent.Label+" doesn't report token usage")
	}
	if e == nil {
		parts = append(parts, billingText(agent, model.ID, nil)...) // the plan, when known
	}
	var descriptions []string
	for _, f := range models.Features(agent, model) {
		name := featureNames[f.Name]
		if f.Available != nil && !*f.Available {
			name = "no " + name
		} else if f.Available == nil {
			name += "?"
		}
		if f.Name == "local" && f.Available != nil && *f.Available {
			name += " per access level"
		} else if f.Source != "" && f.Name == "vision" {
			name += " (" + f.Source + ")"
		}
		descriptions = append(descriptions, name)
	}
	parts = append(parts, strings.Join(descriptions, ", "))
	if e == nil && reports {
		parts = append(parts, `pick "Measure live" to run one short turn per model`)
	}
	return strings.Join(parts, " · ")
}
