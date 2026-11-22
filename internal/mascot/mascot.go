// Package mascot draws MIA, MeetStream's mascot, in the terminal.
//
// The frames come from the agent kit (MS-2268, `mia-state-gifs/mascot-v1`),
// pre-rendered to small RGBA frames per state: idle (a wave), thinking,
// speaking and problem. Each terminal cell is drawn with a half block, so it
// holds two pixels. Colour is true colour when the terminal has it, and the
// 256-colour palette otherwise.
//
// There is no mascot when output isn't a colour terminal, or when
// MEETSTREAM_MASCOT=off.
package mascot

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	_ "embed"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

//go:embed mascot.json
var raw []byte

type data struct {
	Width  int                 `json:"width"`
	Height int                 `json:"height"`
	States map[string][]string `json:"states"`
}

var (
	once   sync.Once
	art    data
	frames map[string][][]byte
)

func load() {
	once.Do(func() {
		frames = map[string][][]byte{}
		if json.Unmarshal(raw, &art) != nil {
			return
		}
		for state, list := range art.States {
			for _, f := range list {
				if b, err := base64.StdEncoding.DecodeString(f); err == nil && len(b) == art.Width*art.Height*4 {
					frames[state] = append(frames[state], b)
				}
			}
		}
	})
}

// Enabled reports whether the mascot should be drawn here.
func Enabled() bool {
	switch strings.ToLower(os.Getenv("MEETSTREAM_MASCOT")) {
	case "off", "none", "0":
		return false
	}
	load()
	return len(frames["idle"]) > 0 && lipgloss.ColorProfile() != termenv.Ascii
}

// Frames returns how many frames a state has (0 when unknown).
func Frames(state string) int { load(); return len(frames[state]) }

// Width is the mascot's width in terminal cells.
func Width() int { load(); return art.Width }

func cube(v byte) int {
	switch {
	case v < 48:
		return 0
	case v < 115:
		return 1
	default:
		return (int(v) - 35) / 40
	}
}

func color(px []byte, bg, full bool) string {
	layer := 38
	if bg {
		layer = 48
	}
	if full {
		return fmt.Sprintf("\x1b[%d;2;%d;%d;%dm", layer, px[0], px[1], px[2])
	}
	return fmt.Sprintf("\x1b[%d;5;%dm", layer, 16+36*cube(px[0])+6*cube(px[1])+cube(px[2]))
}

// Render returns one frame of a state as lines of half blocks.
func Render(state string, frame int) []string {
	load()
	list := frames[state]
	if len(list) == 0 {
		list = frames["idle"]
	}
	if len(list) == 0 {
		return nil
	}
	f := list[frame%len(list)]
	full := lipgloss.ColorProfile() == termenv.TrueColor
	w, h := art.Width, art.Height
	var rows []string
	for y := 0; y < h; y += 2 {
		var b strings.Builder
		for x := 0; x < w; x++ {
			top := f[(y*w+x)*4 : (y*w+x)*4+4]
			var bot []byte
			if y+1 < h {
				bot = f[((y+1)*w+x)*4 : ((y+1)*w+x)*4+4]
			}
			topOn, botOn := top[3] > 0, bot != nil && bot[3] > 0
			switch {
			case topOn && botOn:
				b.WriteString(color(top, false, full) + color(bot, true, full) + "▀\x1b[0m")
			case topOn:
				b.WriteString(color(top, false, full) + "▀\x1b[0m")
			case botOn:
				b.WriteString(color(bot, false, full) + "▄\x1b[0m")
			default:
				b.WriteByte(' ')
			}
		}
		rows = append(rows, b.String())
	}
	return rows
}

// Beside puts MIA on the left and block (any multi-line string) on the right,
// vertically centred. Without a mascot it returns block unchanged.
func Beside(state string, frame int, block string) string {
	if !Enabled() {
		return block
	}
	return lipgloss.JoinHorizontal(lipgloss.Center, strings.Join(Render(state, frame), "\n"), "   ", block)
}
