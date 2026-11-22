#!/bin/sh
# Renders the home app, its doctor pane, the bots / agents / calendar tables,
# a transcript, setup's step 1 (how MIA runs, demo prices) and its model step,
# the bench table and `doctor` to PNGs in docs/images (needs charm freeze).
# Everything runs against a fake home, a fake API (in the tests) with demo
# data, and fake agent CLIs, so no real device, account or path shows.
set -e
cd "$(dirname "$0")/.."
out=$(mktemp -d)
demo=/tmp/meetstream-demo # short, neutral paths in the doctor output
rm -rf "$demo" && mkdir -p "$demo/bin" "$demo/home/meetings" "$demo/kit" "$demo/cfg"
FREEZE="--language ansi --window --theme dracula --padding 20,40 --font.size 15 --border.radius 8"
FORCE_COLOR=1 MEETSTREAM_DUMP_HOME="$out/home" MEETSTREAM_DUMP_APP_DOCTOR="$out/app-doctor" \
	MEETSTREAM_DUMP_BOTS="$out/bots-list" MEETSTREAM_DUMP_MIA="$out/mia-list" MEETSTREAM_DUMP_CALENDAR="$out/calendar-list" MEETSTREAM_DUMP_TRANSCRIPT="$out/transcript" \
	MEETSTREAM_DUMP_SETUP_MODEL="$out/setup-model" MEETSTREAM_DUMP_SETUP_MODE="$out/setup-mode" MEETSTREAM_DUMP_BENCH="$out/bench" \
	go test ./internal/cli -run 'TestHomeView|TestHomeDoctorRunsInsideAndEscReturns|TestHumanViews|TestSetupModelView|TestSetupModeView|TestBenchTableView' -count=1 </dev/null >/dev/null
go build -o dist-local/meetstream ./cmd/meetstream </dev/null

# Fake agent CLI (signed in) and a fake computer name for the relay line.
printf '#!/bin/sh\nexit 0\n' >"$demo/bin/claude"
printf '#!/bin/sh\necho "Demo Mac"\n' >"$demo/bin/scutil"
chmod +x "$demo/bin/claude" "$demo/bin/scutil"
printf '{"device_id":"dev_demo01","refresh_token":"demo","auth_url":"https://auth.example.com","relay_url":"wss://relay.example.com/v1/connect"}' >"$demo/kit/credentials.json"
printf '{"agent":"claude","workdir":"%s","max_access":"readonly","tunnel":"relay","model":"sonnet","effort":"medium"}' "$demo/home/meetings" >"$demo/kit/setup.json"
(cd "$demo/home/meetings" && env -i PATH="$demo/bin" HOME="$demo/home" USER=you FORCE_COLOR=1 \
	MEETSTREAM_CONFIG_DIR="$demo/cfg" MEETSTREAM_AGENT_KIT_HOME="$demo/kit" MEETSTREAM_AGENT_KIT_CREDENTIALS=file \
	MEETSTREAM_API_KEY=ms_demo_key_a1b2 "$OLDPWD/dist-local/meetstream" doctor --no-warm </dev/null >"$out/doctor" 2>&1) || true

for shot in home app-doctor bots-list mia-list calendar-list transcript setup-model setup-mode bench doctor; do
	freeze "$out/$shot" $FREEZE -o "docs/images/$shot.png" </dev/null
done
rm -rf "$out" "$demo"
