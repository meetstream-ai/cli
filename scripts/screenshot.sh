#!/bin/sh
# Renders the home screen and `doctor` to PNGs in docs/images (needs charm freeze).
set -e
cd "$(dirname "$0")/.."
home=$(mktemp); doc=$(mktemp); cfg=$(mktemp -d)
FREEZE="--language ansi --window --theme dracula --padding 20,40 --font.size 15 --border.radius 8"
FORCE_COLOR=1 MEETSTREAM_DUMP_HOME="$home" go test ./internal/cli -run TestHomeView -count=1 </dev/null >/dev/null
go build -o dist-local/meetstream ./cmd/meetstream </dev/null
FORCE_COLOR=1 MEETSTREAM_CONFIG_DIR="$cfg" MEETSTREAM_API_KEY=ms_demo_key_a1b2 ./dist-local/meetstream doctor </dev/null >"$doc"
freeze "$home" $FREEZE -o docs/images/home.png </dev/null
freeze "$doc" $FREEZE -o docs/images/doctor.png </dev/null
rm -rf "$home" "$doc" "$cfg"
