package bench

import (
	"context"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// Esc stops and keeps results; ctrl+c does the same but marks the run
// interrupted, so the command exits 130.
func TestCtrlCInterruptsTheLiveTable(t *testing.T) {
	run := func(ctx context.Context, r Request) Measured {
		<-ctx.Done()
		return Measured{Model: r.Model, Error: "cancelled"}
	}
	for key, want := range map[tea.KeyType]bool{tea.KeyEsc: false, tea.KeyCtrlC: true} {
		q := NewQueue([]string{"a"}, run, Options{Parallel: 1})
		var m tea.Model = NewModel(context.Background(), q)
		m, _ = m.Update(tea.KeyMsg{Type: key})
		if !m.(Model).Stopped() || m.(Model).Interrupted() != want {
			t.Fatalf("%v: stopped=%v interrupted=%v", key, m.(Model).Stopped(), m.(Model).Interrupted())
		}
	}
}
