// Package meetstream embeds the docs the CLI shows offline (`meetstream docs`).
package meetstream

import _ "embed"

//go:embed README.md
var Readme string

//go:embed SKILL.md
var Skill string
