// Package redact hides obvious secrets (API keys, bearer tokens) in text the
// bridge prints: meeting turns and agent errors can quote them.
package redact

import "regexp"

// Hidden replaces each secret.
const Hidden = "[redacted]"

var tokens = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]{8,}`),
	regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{8,}`), // OpenAI, Anthropic (sk-ant-…)
	regexp.MustCompile(`\bms_[A-Za-z0-9_-]{8,}`), // MeetStream
	regexp.MustCompile(`\b(ghp|gho|ghu|ghs|ghr|github_pat)_[A-Za-z0-9_]{16,}`),
	regexp.MustCompile(`\bxox[abposr]-[A-Za-z0-9-]{10,}`),                                 // Slack
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),                                            // AWS access key id
	regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}`),                                         // Google API key
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`), // JWT
}

// keyValue is "api_key=…", "password: …" and the like; the name stays.
var keyValue = regexp.MustCompile(`(?i)\b(api[_-]?key|access[_-]?token|auth[_-]?token|secret|password|passwd|token)(["']?\s*[:=]\s*["']?)[^\s"',;]{6,}`)

// Secrets returns s with each secret replaced by Hidden.
func Secrets(s string) string {
	for _, p := range tokens {
		s = p.ReplaceAllString(s, Hidden)
	}
	return keyValue.ReplaceAllString(s, "${1}${2}"+Hidden)
}
