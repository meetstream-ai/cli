package models

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// top-level config.yaml blocks whose provider: + model: pairs Hermes can run (not stt, tts…)
var hermesSections = map[string]string{
	"model":              "alias",
	"fallback_providers": "fallback",
	"delegation":         "delegation",
	"moa":                "MoA",
}

type yamlFrame struct {
	indent  int
	parent  string
	section string
	values  map[string]string
}

var (
	dquoted = regexp.MustCompile(`^"(.*?)"(\s+#.*)?$`)
	squoted = regexp.MustCompile(`^'(.*?)'(\s+#.*)?$`)
	comment = regexp.MustCompile(`\s+#`)
)

func yamlScalar(value string) string {
	value = strings.TrimSpace(value)
	if m := dquoted.FindStringSubmatch(value); m != nil {
		return m[1]
	}
	if m := squoted.FindStringSubmatch(value); m != nil {
		return m[1]
	}
	return strings.TrimSpace(comment.Split(value, 2)[0])
}

// HermesModelsFrom reads the provider:model pairs in Hermes' config.yaml: aliases,
// fallbacks, delegation, MoA (stt/tts and other blocks are skipped). `hermes -m
// provider:model` and ACP's session/set_model accept these ids. A small reader
// for the block mappings and "- provider:" list items Hermes writes.
func HermesModelsFrom(text string) []Choice {
	var frames, every []*yamlFrame
	section := ""
	openKeys := map[int]string{} // "key:" with no value, by indent: the parent of what follows
	for _, raw := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		left := strings.TrimLeftFunc(raw, unicode.IsSpace)
		if strings.TrimSpace(raw) == "" || strings.HasPrefix(left, "#") {
			continue
		}
		indent := len(raw) - len(left)
		content := strings.TrimSpace(raw)
		if indent == 0 {
			name, _, _ := strings.Cut(content, ":")
			section, frames, openKeys = strings.TrimSpace(name), nil, map[int]string{}
			continue
		}
		if _, ok := hermesSections[section]; !ok {
			continue
		}
		item := strings.HasPrefix(content, "- ")
		if item {
			content, indent = strings.TrimSpace(content[2:]), indent+2
		}
		for len(frames) > 0 && (frames[len(frames)-1].indent > indent || (item && frames[len(frames)-1].indent == indent)) {
			frames = frames[:len(frames)-1]
		}
		if len(frames) == 0 || frames[len(frames)-1].indent < indent {
			parent, best := "", -1
			for i, k := range openKeys {
				if i < indent && i > best {
					parent, best = k, i
				}
			}
			f := &yamlFrame{indent: indent, parent: parent, section: section, values: map[string]string{}}
			frames = append(frames, f)
			every = append(every, f)
		}
		key, value, ok := strings.Cut(content, ":")
		if !ok {
			continue
		}
		key, value = strings.TrimSpace(key), yamlScalar(value)
		for i := range openKeys {
			if i >= indent {
				delete(openKeys, i)
			}
		}
		if value != "" {
			frames[len(frames)-1].values[key] = value
		} else {
			openKeys[indent] = key
		}
	}
	var found []Choice
	for _, f := range every {
		provider, model := f.values["provider"], f.values["model"]
		if provider == "" || model == "" {
			continue
		}
		label := hermesSections[f.section]
		if label == "alias" && f.parent != "" {
			label = "alias " + f.parent
		}
		found = append(found, Choice{ID: provider + ":" + model, Label: label})
	}
	return Unique(found)
}

// HermesCatalog is Hermes' models from its own files under home ("" =
// os.UserHomeDir), without starting it: its ACP model list can take over 30 s.
// A note counts the extra models in provider_models_cache.json.
func HermesCatalog(home string) Catalog {
	root := filepath.Join(userHome(home), ".hermes")
	text, ok := readText(filepath.Join(root, "config.yaml"))
	if !ok {
		return Catalog{Error: "no ~/.hermes/config.yaml"}
	}
	catalog := Catalog{Models: HermesModelsFrom(text), Source: "~/.hermes/config.yaml"}
	listed := map[string]bool{}
	for _, m := range catalog.Models {
		listed[m.ID] = true
	}
	main := YAMLBlockValue(text, "model", "provider")
	var extra []string
	for _, p := range orderedObject(filepath.Join(root, "provider_models_cache.json")) {
		var entry struct {
			Models []any `json:"models"`
		}
		if json.Unmarshal(p.value, &entry) != nil || entry.Models == nil {
			continue
		}
		for _, m := range entry.Models {
			if s, ok := m.(string); ok && s != "auto" {
				if id := p.key + ":" + s; !listed[id] {
					extra = append(extra, id)
				}
			}
		}
	}
	sort.SliceStable(extra, func(i, j int) bool {
		return strings.HasPrefix(extra[i], main+":") && !strings.HasPrefix(extra[j], main+":")
	})
	if len(extra) > 0 {
		catalog.Note = fmt.Sprintf("Hermes knows %d more models from your signed-in providers. To use one, pick "+
			"\"Type another model id\" and enter provider:model, for example %s.", len(extra), extra[0])
	}
	return catalog
}

type keyValue struct {
	key   string
	value json.RawMessage
}

// orderedObject is a JSON file's top-level object in file order; nil when unreadable.
func orderedObject(path string) []keyValue {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil
	}
	var out []keyValue
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return out
		}
		key, _ := tok.(string)
		var raw json.RawMessage
		if dec.Decode(&raw) != nil {
			return out
		}
		out = append(out, keyValue{key, raw})
	}
	return out
}

func readJSONObject(path string) map[string]any {
	data, err := os.ReadFile(path)
	if err != nil {
		return map[string]any{}
	}
	var m map[string]any
	if json.Unmarshal(data, &m) != nil || m == nil {
		return map[string]any{}
	}
	return m
}
