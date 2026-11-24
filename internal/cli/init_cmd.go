package cli

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/meetstream-ai/cli/internal/templates"
	"github.com/meetstream-ai/cli/internal/ui"
)

var (
	llmChoices   = []string{"anthropic", "openai", "none"}
	emailChoices = []string{"resend", "sendgrid", "smtp", "none"}
	langLabels   = map[string]string{"python": "Python", "node": "Node.js", "none": "none"}
)

type initChoices struct {
	UseCase, Lang, Transcription, Live, LLM, Email, Dir string
}

func hasOption(u templates.UseCase, o string) bool { return contains(u.Options, o) }

// validateInit checks every choice; with --no-input it names the first missing flag.
func validateInit(c *initChoices) (templates.UseCase, error) {
	if c.UseCase == "" {
		return templates.UseCase{}, usageErr("missing --use-case", "meetstream init --list --json shows the choices")
	}
	u, ok := templates.Find(c.UseCase)
	if !ok {
		var ids []string
		for _, x := range templates.Catalog {
			ids = append(ids, x.ID)
		}
		return u, usageErr("unknown use case "+c.UseCase, "choose: "+strings.Join(ids, ", "))
	}
	if !u.Available {
		return u, &ExitError{Code: 3, Message: u.Title + " has no template yet", Hint: u.Summary}
	}
	if c.Lang == "" {
		c.Lang = u.Langs[0]
	}
	if !contains(u.Langs, c.Lang) {
		return u, usageErr(c.UseCase+" has no "+c.Lang+" template", "choose --lang "+strings.Join(u.Langs, " or "))
	}
	if hasOption(u, "transcription") {
		if c.Transcription == "" {
			c.Transcription = "meetstream"
		}
		if _, known := postCallProvider(c.Transcription); !known || isStreamingOnly(c.Transcription) {
			return u, usageErr("unknown --transcription "+c.Transcription, "choose: "+strings.Join(providerIDs(postCallProviders), ", "))
		}
	}
	if hasOption(u, "live-transcription") {
		if c.Live == "" {
			c.Live = "meetstream"
		}
		if _, err := liveProvider(c.Live); err != nil {
			return u, err
		}
	}
	if hasOption(u, "llm") {
		if c.LLM == "" {
			c.LLM = "anthropic"
		}
		if !contains(llmChoices, c.LLM) {
			return u, usageErr("unknown --llm "+c.LLM, "choose: "+strings.Join(llmChoices, ", "))
		}
	}
	if hasOption(u, "email") {
		if c.Email == "" {
			c.Email = "resend"
		}
		if !contains(emailChoices, c.Email) {
			return u, usageErr("unknown --email "+c.Email, "choose: "+strings.Join(emailChoices, ", "))
		}
	}
	if c.Dir == "" {
		c.Dir = "./" + c.UseCase
	}
	return u, nil
}

// askInit walks the questions as steps; Esc goes back one step.
func askInit(c *initChoices) error {
	type step struct {
		skip func() bool
		run  func() (menuResult, error)
	}
	pick := func(question string, opts, hints []string, current string, set func(string), first bool) (menuResult, error) {
		labels := make([]string, len(opts))
		cursor := 0
		for i, o := range opts {
			labels[i] = o
			if o == current {
				cursor = i
			}
		}
		m := newStepMenu(question, labels...).withHints(hints).at(cursor)
		m.noBack = first
		res, i, err := ask(m)
		if res == menuChosen {
			set(opts[i])
		}
		return res, err
	}
	u := func() templates.UseCase { x, _ := templates.Find(c.UseCase); return x }
	steps := []step{
		{run: func() (menuResult, error) {
			var ids, titles, hints []string
			var disabled []bool
			for _, x := range templates.Catalog {
				ids, titles, hints = append(ids, x.ID), append(titles, x.Title), append(hints, x.Summary)
				disabled = append(disabled, !x.Available)
			}
			cursor := 0
			for i, id := range ids {
				if id == c.UseCase {
					cursor = i
				}
			}
			m := newStepMenu("What do you want to build?", titles...).withHints(hints).at(cursor)
			m.disabled, m.noBack = disabled, true
			res, i, err := ask(m)
			if res == menuChosen && ids[i] != c.UseCase {
				*c = initChoices{UseCase: ids[i], Dir: c.Dir}
			}
			return res, err
		}},
		{skip: func() bool { return len(u().Langs) < 2 }, run: func() (menuResult, error) {
			var hints []string
			for range u().Langs {
				hints = append(hints, "no packages to install")
			}
			return pick("Language", u().Langs, hints, c.Lang, func(v string) { c.Lang = v }, false)
		}},
		{skip: func() bool { return !hasOption(u(), "transcription") }, run: func() (menuResult, error) {
			ids := []string{"meetstream", "deepgram", "assemblyai", "sarvam"}
			hints := []string{"recommended · no key needed", "your key, saved in the dashboard", "your key, saved in the dashboard", "your key · Indian languages"}
			return pick("Transcription after the call", ids, hints, c.Transcription, func(v string) { c.Transcription = v }, false)
		}},
		{skip: func() bool { return !hasOption(u(), "live-transcription") }, run: func() (menuResult, error) {
			ids := providerIDs(liveProviders)
			hints := []string{"no key needed", "your key, saved in the dashboard", "your key, saved in the dashboard", "your key, saved in the dashboard"}
			return pick("Live transcription", ids, hints, c.Live, func(v string) { c.Live = v }, false)
		}},
		{skip: func() bool { return !hasOption(u(), "llm") }, run: func() (menuResult, error) {
			return pick("Model that writes the notes", llmChoices, []string{"your key goes in .env", "your key goes in .env", "plain notes, no model"}, c.LLM, func(v string) { c.LLM = v }, false)
		}},
		{skip: func() bool { return !hasOption(u(), "email") }, run: func() (menuResult, error) {
			return pick("Send the notes by", emailChoices, []string{"API key in .env", "API key in .env", "your mail server", "print them"}, c.Email, func(v string) { c.Email = v }, false)
		}},
		{run: func() (menuResult, error) {
			def := c.Dir
			if def == "" {
				def = "./" + c.UseCase
			}
			res, v, err := askText("Folder", def)
			if res == menuChosen {
				c.Dir = v
			}
			return res, err
		}},
	}
	for i := 0; i < len(steps); {
		if steps[i].skip != nil && steps[i].skip() {
			i++
			continue
		}
		res, err := steps[i].run()
		if err != nil {
			return err
		}
		switch res {
		case menuQuit:
			return errQuit
		case menuBack:
			if i == 0 {
				return errQuit
			}
			for i--; i > 0 && steps[i].skip != nil && steps[i].skip(); i-- {
			}
			continue
		}
		i++
	}
	return nil
}

func newInitCmd() *cobra.Command {
	var c initChoices
	var list, yes, force, noEnv, withKey bool
	cmd := &cobra.Command{
		Use: "init [use-case]", Short: "Start a project from a template: notetaker, send-a-bot, live captions, webhooks", GroupID: "start",
		Args: cobra.MaximumNArgs(1),
		Long: `Start a project from a MeetStream template.

In a terminal it asks: what to build, language, transcription, extras, folder
(Esc goes back a step). Every question is also a flag, so agents and scripts
run it with --yes --no-input. --list --json prints the catalog.

It writes the files, a .env with your MeetStream key filled in (never shown),
an AGENTS.md for coding agents, and prints the next commands.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if list {
				return emit(map[string]any{"use_cases": templates.Catalog, "transcription": providerIDs(postCallProviders),
					"live_transcription": providerIDs(liveProviders), "llm": llmChoices, "email": emailChoices}, func() {
					for _, u := range templates.Catalog {
						state := strings.Join(u.Langs, ", ")
						if !u.Available {
							state = "coming soon"
						}
						fmt.Fprintf(ui.Out, "  %-18s %-58s %s\n", u.ID, ui.DimStyle.Render(u.Summary), state)
					}
				})
			}
			if len(args) == 1 {
				c.UseCase = args[0]
			}
			interactive := !g.NoInput && !g.JSON && !yes && ui.Interactive()
			if interactive {
				ui.Header(cmd.Root().Version, "Start a project")
				if err := askInit(&c); err != nil {
					if errors.Is(err, errQuit) {
						return &ExitError{Code: 130, Message: "cancelled", Silent: true}
					}
					return err
				}
			}
			u, err := validateInit(&c)
			if err != nil {
				return err
			}
			d := templates.Data{UseCase: u.ID, Lang: c.Lang, Title: "MeetStream " + strings.ToLower(u.Title), BotName: "MeetStream Notetaker",
				Transcription: c.Transcription, Live: c.Live, LLM: c.LLM, Email: c.Email}
			plan, err := templates.Build(d)
			if err != nil {
				return err
			}
			if err := templates.Write(c.Dir, plan, force); err != nil {
				var ne *templates.NotEmptyError
				if errors.As(err, &ne) {
					return usageErr(c.Dir+" is not empty", "use another --dir, or --force to overwrite only the template's files")
				}
				return err
			}
			rep := envReport{}
			if !noEnv {
				rep, err = syncEnv(c.Dir, true, false, withKey)
				if err != nil {
					return err
				}
			}
			next := nextSteps(u.ID, c)
			abs, _ := filepath.Abs(c.Dir)
			return emit(map[string]any{"use_case": u.ID, "lang": c.Lang, "dir": c.Dir, "path": abs, "template_version": cmd.Root().Version,
				"options": map[string]string{"transcription": c.Transcription, "live_transcription": c.Live, "llm": c.LLM, "email": c.Email},
				"files":   plan.Paths(), "env": map[string]any{"meetstream_auth": rep.Mode, "filled": rep.Filled, "missing": rep.Missing}, "next": next}, func() {
				fmt.Fprintln(ui.Out)
				ui.OK(fmt.Sprintf("Template %s (%s) → %s", u.ID, langLabels[c.Lang], c.Dir))
				ui.OK(fmt.Sprintf("%d files, including AGENTS.md for coding agents", len(plan.Files)))
				if !noEnv {
					msg := ".env written: no MeetStream key in it (the app uses your CLI sign-in)"
					if rep.Mode == "key" {
						msg = ".env written: MeetStream key and API URL from your profile"
					}
					if len(rep.Missing) > 0 {
						msg += ui.DimStyle.Render(fmt.Sprintf("  (%d key(s) still empty: %s)", len(rep.Missing), strings.Join(rep.Missing, ", ")))
					}
					ui.OK(msg)
				}
				fmt.Fprintln(ui.Out)
				fmt.Fprintln(ui.Out, "  "+ui.BoldStyle.Render("Next"))
				for _, s := range next {
					fmt.Fprintln(ui.Out, "    "+ui.CmdStyle.Render(s))
				}
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&c.UseCase, "use-case", "", "notetaker, send-a-bot, live-captions, webhook-receiver")
	f.StringVar(&c.Lang, "lang", "", "python or node (per use case)")
	f.StringVar(&c.Transcription, "transcription", "", "post-call provider (default meetstream)")
	f.StringVar(&c.Live, "live-transcription", "", "live provider for live-captions (default meetstream)")
	f.StringVar(&c.LLM, "llm", "", "notes model: anthropic, openai, none")
	f.StringVar(&c.Email, "email", "", "send notes by: resend, sendgrid, smtp, none")
	f.StringVar(&c.Dir, "dir", "", "folder (default ./<use-case>)")
	f.BoolVarP(&yes, "yes", "y", false, "take the recommended defaults; ask nothing")
	f.BoolVar(&force, "force", false, "write into a non-empty folder (overwrites only the template's files)")
	f.BoolVar(&noEnv, "no-env", false, "do not write .env")
	f.BoolVar(&withKey, "with-key", false, "put your MeetStream key in .env (for servers; default: the app uses your CLI sign-in)")
	f.BoolVar(&list, "list", false, "list the templates")
	return cmd
}

func nextSteps(id string, c initChoices) []string {
	cd := "cd " + c.Dir
	switch id {
	case "notetaker":
		return []string{cd, "meetstream env --check", "python app.py", "meetstream listen --forward-to http://127.0.0.1:8000/webhook   # add --tunnel cloudflared for real bots",
			"meetstream join <meeting-url> --transcription " + c.Transcription + " --follow"}
	case "send-a-bot":
		run := "python send_bot.py <meeting-url>"
		if c.Lang == "node" {
			run = "node send_bot.mjs <meeting-url>"
		}
		return []string{cd, run}
	case "live-captions":
		return []string{cd, "python app.py", "meetstream listen --forward-to http://127.0.0.1:8000/webhook --tunnel cloudflared",
			"meetstream join <meeting-url> --live-transcription " + c.Live}
	case "webhook-receiver":
		run := "python app.py"
		if c.Lang == "node" {
			run = "node app.mjs"
		}
		return []string{cd, run, "meetstream listen --forward-to http://127.0.0.1:8000/webhook", "meetstream trigger bot.notallowed"}
	}
	return []string{cd}
}
