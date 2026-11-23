package meetingctx

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExpandBothSeparators(t *testing.T) {
	home, _ := os.UserHomeDir()
	for in, want := range map[string]string{
		"~":              home,
		"~/notes":        filepath.Join(home, "notes"),
		`~\MeetStream`:   filepath.Join(home, `\MeetStream`), // on Windows: home\MeetStream
		"/abs/~/x":       "/abs/~/x",
		"~other/x":       "~other/x",
		"relative/~/dir": "relative/~/dir",
	} {
		if got := Expand(in); got != want {
			t.Errorf("Expand(%q) = %q, want %q", in, got, want)
		}
	}
}
