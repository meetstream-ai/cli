package main

import (
	"os"

	"github.com/meetstream-ai/cli/internal/cli"
)

// Set by goreleaser through -ldflags.
var (
	version = "0.0.0-dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	os.Exit(cli.Execute(cli.BuildInfo{Version: version, Commit: commit, Date: date}))
}
