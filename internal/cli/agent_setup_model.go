package cli

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/meetstream-ai/cli/internal/agentkit/agents"
	"github.com/meetstream-ai/cli/internal/agentkit/bench"
	"github.com/meetstream-ai/cli/internal/agentkit/estimates"
	"github.com/meetstream-ai/cli/internal/agentkit/models"
	"github.com/meetstream-ai/cli/internal/agentkit/pricing"
	"github.com/meetstream-ai/cli/internal/agentkit/usage"
	"github.com/meetstream-ai/cli/internal/ui"
)

// Setup's step 2: which model the agent CLI uses in meetings, and how hard it
// thinks (the kit's _pick_model and _pick_effort).

// setupCatalog lists the agent's models; tests swap it so no CLI is started.
var setupCatalog = func(spec agents.Spec, binary string) models.Catalog {
	return models.CatalogFor(spec, binary, models.Options{})
}

// setupShared is MeetStream's shared estimates: fetched at most once a day,
// the embedded copy when offline. Tests swap it so nothing is fetched.
var setupShared = func() []estimates.Entry { return estimates.LoadShared(estimates.SharedOptions{}) }

// Menu texts of the model step.
const (
	typeModelLabel  = "Type another model id"
	typeModelDetail = "Enter a model id from your CLI."
	measureLabel    = "Measure live (one short real turn per model)"
	measureDetail   = "Choose models to measure. Each runs one short real turn and uses your plan or API credits."
	effortQuestion  = "Effort (how hard it thinks)"
	cliDefaultLabel = "The CLI's default"
)

type modelPick struct {
	model, effort string
	asked         bool // false when there was nothing to choose (Back skips the step)
}

// pickModel asks for the model, then the effort when the CLI lists efforts.
// "" means the CLI's own default. Back from the effort returns to the model menu.
func (w *setupWizard) pickModel(spec agents.Spec, binary, lastModel, lastEffort string) (modelPick, menuResult, error) {
	stop := spin("Asking " + spec.Label + " which models it offers")
	listed := setupCatalog(spec, binary)
	stop()
	// "N models from <source>"; the source is never blank.
	if len(listed.Models) > 0 {
		ui.OK(listed.Summary(spec.Label))
	} else {
		ui.Warn(listed.Summary(spec.Label))
	}
	if listed.Note != "" {
		ui.Note(listed.Note)
	}
	if only, ok := listed.AutoOnly(); ok {
		// Nothing to choose (Copilot offers only Auto). Only for "auto": a CLI that
		// lists just its default still gets the menu, since it takes other ids.
		ui.OK(models.AutoLine(spec.Label))
		if len(listed.EffortsFor(only.ID)) == 0 {
			return modelPick{model: only.ID}, menuChosen, nil
		}
		effort, res, err := pickEffort(listed, only.ID, lastEffort)
		return modelPick{only.ID, effort, true}, res, err
	}
	shared := setupShared()
	for {
		menu, shown, measurable := modelMenu(spec, listed, lastModel, estimates.LoadWithShared(spec.Name, shared))
		res, i, err := ask(menu)
		if err != nil || res != menuChosen {
			return modelPick{asked: true}, res, err
		}
		var model string
		switch {
		case i == len(shown)+2: // Measure live
			if r, err := w.measureLive(spec, binary, listed, measurable, lastEffort); err != nil || r == menuQuit {
				return modelPick{asked: true}, r, err
			}
			continue
		case i == len(shown): // Type another model id
			r, typed, err := askText("Model id (as the CLI names it)", lastModel)
			if err != nil || r == menuQuit {
				return modelPick{asked: true}, menuQuit, err
			}
			if r == menuBack || typed == "" {
				continue
			}
			model = typed
		case i < len(shown):
			model = shown[i].ID
		} // else: the CLI's default ("")
		effort, r, err := pickEffort(listed, model, lastEffort)
		if err != nil || r == menuQuit {
			return modelPick{asked: true}, menuQuit, err
		}
		if r == menuBack { // back from the effort question: the model menu again
			lastModel = model
			continue
		}
		return modelPick{model, effort, true}, menuChosen, nil
	}
}

// modelMenu builds the model step's menu: one row per model (usage bar,
// tokens/h, ≈cost or —, plan/free/api tag, feature chips, first words), then
// "Type another model id", the CLI's default, and "Measure live" for CLIs that
// report usage. Every row has a detail line; the Back row has its own.
func modelMenu(spec agents.Spec, listed models.Catalog, lastModel string, cached map[string]estimates.Estimate) (stepMenuModel, []models.Choice, []models.Choice) {
	shown := models.MenuModels(listed, lastModel)
	cliDefault := models.Choice{Vision: listed.Vision, VisionSource: listed.VisionSource}
	for _, m := range listed.Models {
		if m.ID == listed.DefaultModel {
			cliDefault.Vision, cliDefault.VisionSource = m.Vision, m.VisionSource
		}
	}
	measurable := append(append([]models.Choice{}, shown...), cliDefault)
	var largest int64
	for _, m := range measurable {
		if e, ok := cached[m.ID]; ok {
			largest = max(largest, usage.ProjectHour(e.Usage, nil).Total())
		}
	}
	estimate := func(id string) *estimates.Estimate {
		if e, ok := cached[id]; ok {
			return &e
		}
		return nil
	}
	rows := make([]estimates.Row, len(measurable))
	idWidth := len(cliDefaultLabel)
	for i, m := range measurable {
		rows[i] = estimates.RowFor(spec, m, estimate(m.ID), largest)
		idWidth = max(idWidth, min(lipgloss.Width(rows[i].Model), 30))
	}
	var labels, details []string
	for i, m := range shown {
		labels = append(labels, modelRowText(rows[i], idWidth))
		details = append(details, estimates.DetailText(spec, m, estimate(m.ID)))
	}
	labels = append(labels, typeModelLabel, modelRowText(rows[len(shown)], idWidth))
	details = append(details, typeModelDetail, estimates.DetailText(spec, cliDefault, estimate("")))
	if usage.ReportsUsage(spec.Name) { // measuring needs the CLI's own token counts
		labels = append(labels, measureLabel)
		details = append(details, measureDetail)
	}
	wanted := lastModel
	if wanted == "" {
		wanted = listed.DefaultModel
	}
	cursor := len(shown) + 1 // the CLI's default
	for i, m := range shown {
		if m.ID == wanted && wanted != "" {
			cursor = i
		}
	}
	return newStepMenu("Which model?", labels...).withDetails(details).at(cursor), shown, measurable
}

var (
	barStyle  = lipgloss.NewStyle().Foreground(ui.Cyan)
	planStyle = lipgloss.NewStyle().Foreground(ui.Green)
	apiStyle  = lipgloss.NewStyle().Foreground(ui.Yellow)
)

// modelRowText is one model row, coloured; widths ignore the colour codes.
func modelRowText(r estimates.Row, idWidth int) string {
	pad := func(s string, w int) string { return s + strings.Repeat(" ", max(0, w-lipgloss.Width(s))) }
	bar := r.Bar + strings.Repeat(" ", max(0, 10-len([]rune(r.Bar))))
	kind := ""
	switch r.Kind {
	case pricing.Plan:
		kind = planStyle.Render("plan")
	case pricing.API:
		kind = apiStyle.Render("api")
	}
	cost := r.CostText
	if r.Kind == pricing.Free {
		cost = planStyle.Render("free")
	}
	chips := make([]string, len(r.Chips))
	for i, c := range r.Chips {
		switch c.State {
		case estimates.ChipOn:
			chips[i] = planStyle.Render(c.Label)
		case estimates.ChipOff:
			chips[i] = ui.DimStyle.Render(c.Label)
		default:
			chips[i] = apiStyle.Render(c.Label)
		}
	}
	return pad(bench.Clip(r.Model, idWidth), idWidth) + "  [" + barStyle.Render(bar) + "] " + pad(r.Usage, 12) + " " +
		pad(cost, 7) + " " + pad(kind, 4) + "  " + strings.Join(chips, " ") + "  " + ui.DimStyle.Render(r.FirstWords)
}

// pickEffort asks for the effort when the CLI lists efforts for model.
func pickEffort(listed models.Catalog, model, last string) (string, menuResult, error) {
	efforts := listed.EffortsFor(model)
	if len(efforts) == 0 {
		return "", menuChosen, nil
	}
	labels := append([]string{cliDefaultLabel}, efforts...)
	hints := []string{""}
	if listed.DefaultEffort != "" {
		hints[0] = "now: " + listed.DefaultEffort
	}
	details := []string{"Leave the effort to the CLI's own setting."}
	cursor := 0
	for i, e := range efforts {
		hint := ""
		if e == listed.DefaultEffort {
			hint = "your CLI's setting"
		}
		hints = append(hints, hint)
		details = append(details, "Think at "+e+" effort: higher answers better and slower, and uses more tokens.")
		if e == last {
			cursor = i + 1
		}
	}
	res, i, err := ask(newStepMenu(effortQuestion, labels...).withHints(hints).withDetails(details).at(cursor))
	if err != nil || res != menuChosen {
		return "", res, err
	}
	if i == 0 {
		return "", menuChosen, nil
	}
	return efforts[i-1], menuChosen, nil
}

// measureLive runs "Measure live": tick models (unmeasured ones first, up to
// bench.DefaultTicked), ask before more than bench.AskAbove, then measure them
// with the live table and print the chart. menuBack/menuChosen both return to
// the model menu; menuQuit cancels setup.
func (w *setupWizard) measureLive(spec agents.Spec, binary string, listed models.Catalog, measurable []models.Choice, effort string) (menuResult, error) {
	cached := estimates.Load(spec.Name)
	order := make([]int, len(measurable))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		_, ma := cached[measurable[order[a]].ID]
		_, mb := cached[measurable[order[b]].ID]
		return !ma && mb
	})
	ticked := order[:min(len(order), bench.DefaultTicked)]
	names, hints := make([]string, len(measurable)), make([]string, len(measurable))
	for i, m := range measurable {
		names[i], hints[i] = orDefault(m.ID, cliDefaultLabel), m.Label
		if hints[i] == m.ID {
			hints[i] = ""
		}
	}
	final, err := runTea(newChecklist("Models to measure (one short real turn each)", names, hints, ticked))
	if err != nil {
		return menuQuit, err
	}
	cl := final.(checklistModel)
	if cl.result != menuChosen || len(cl.picked()) == 0 {
		return cl.result, nil
	}
	picked := cl.picked()
	if bench.NeedsConfirm(len(picked)) {
		warning, question := bench.ConfirmText(len(picked))
		ui.Warn(warning)
		res, i, err := askStep(question, "No, pick again", fmt.Sprintf("Yes, measure all %d", len(picked)))
		if err != nil || res == menuQuit {
			return menuQuit, err
		}
		if res != menuChosen || i == 0 {
			return menuBack, nil
		}
	}
	ids := make([]string, len(picked))
	for i, p := range picked {
		ids[i] = measurable[p].ID
	}
	// Each model gets the effort only when the CLI lists it for that model.
	m := newMeasurer(spec, binary)
	run := func(ctx context.Context, req bench.Request) bench.Measured {
		if !contains(listed.EffortsFor(req.Model), req.Effort) {
			req.Effort = ""
		}
		return m.run(ctx, req)
	}
	jobs, _ := runBenchQueue(w.cmd.Context(), spec, ids, run, m, bench.Options{Effort: effort}, true)
	fmt.Fprintln(ui.Out)
	for _, line := range bench.ChartLines(bench.Results(jobs), bench.ChartOptions{}) {
		fmt.Fprintln(ui.Out, line)
	}
	fmt.Fprintln(ui.Out)
	return menuChosen, nil
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}
