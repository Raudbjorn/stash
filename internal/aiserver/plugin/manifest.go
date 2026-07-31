package plugin

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v2"
)

// ManifestFile is the filename inside a plugin directory.
const ManifestFile = "plugin.yml"

// Manifest describes an installed plugin.
//
// Parsing happens once, here, in Go. The Python host is handed the parsed
// result rather than the file, so there is exactly one interpretation of a
// manifest rather than two that can drift.
type Manifest struct {
	Name            string
	Version         string
	RequiredBackend string
	Description     string
	HumanName       string
	ServerLink      string

	// Files are the module basenames to import, without the .py extension.
	Files []string

	// DependsOn names other plugins that must load first.
	DependsOn []string

	// PipDependencies are package specifiers to install into the plugin venv.
	PipDependencies []string

	// Settings are the plugin's declared settings, in declaration order.
	Settings []SettingDef

	// RoutePrefix is parsed but no longer honoured. Plugins now serve under
	// /api/v1/plugins/<their own name>/, because letting a plugin choose its
	// own mount point let it shadow the server's own endpoints. It is still
	// read so an existing manifest declaring one is not rejected.
	RoutePrefix string

	// Dir is where the manifest was read from.
	Dir string
}

// SettingDef is one declared setting.
type SettingDef struct {
	Key         string
	Type        string
	Label       string
	Description string
	Default     any
	Options     []any
}

// rawManifest mirrors the YAML, tolerating the alias spellings that have
// accumulated across plugin generations.
type rawManifest struct {
	Name string `yaml:"name"`

	Version string `yaml:"version"`

	RequiredBackend  string `yaml:"required_backend"`
	RequiredBackend2 string `yaml:"requiredBackend"`

	Description string `yaml:"description"`

	HumanName  string `yaml:"human_name"`
	HumanName2 string `yaml:"humanName"`
	HumanName3 string `yaml:"title"`
	HumanName4 string `yaml:"label"`

	ServerLink  string `yaml:"server_link"`
	ServerLink2 string `yaml:"serverLink"`

	Files any `yaml:"files"`

	DependsOn  any `yaml:"depends_on"`
	DependsOn2 any `yaml:"dependsOn"`

	PipDependencies  any `yaml:"pip_dependencies"`
	PipDependencies2 any `yaml:"pip-dependencies"`
	PipDependencies3 any `yaml:"python_dependencies"`

	// Settings appears in two shapes across the ecosystem: a mapping keyed by
	// setting name, and a list of objects each carrying its own key. Both are
	// accepted.
	Settings any `yaml:"settings"`

	Routes any `yaml:"routes"`
}

// ParseManifestFile reads and validates a plugin.yml.
func ParseManifestFile(path string) (Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, fmt.Errorf("read manifest: %w", err)
	}
	m, err := ParseManifest(data)
	if err != nil {
		return Manifest{}, fmt.Errorf("%s: %w", path, err)
	}
	m.Dir = filepath.Dir(path)
	return m, nil
}

// ParseManifest parses manifest bytes.
func ParseManifest(data []byte) (Manifest, error) {
	var raw rawManifest
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return Manifest{}, fmt.Errorf("parse manifest: %w", err)
	}

	m := Manifest{
		Name:            strings.TrimSpace(raw.Name),
		Version:         strings.TrimSpace(raw.Version),
		RequiredBackend: firstNonEmpty(raw.RequiredBackend, raw.RequiredBackend2),
		Description:     raw.Description,
		HumanName:       firstNonEmpty(raw.HumanName, raw.HumanName2, raw.HumanName3, raw.HumanName4),
		ServerLink:      firstNonEmpty(raw.ServerLink, raw.ServerLink2),
		Files:           stringList(raw.Files),
		DependsOn:       stringList(firstNonNil(raw.DependsOn, raw.DependsOn2)),
		PipDependencies: stringList(firstNonNil(raw.PipDependencies, raw.PipDependencies2, raw.PipDependencies3)),
	}

	if m.Name == "" {
		return Manifest{}, fmt.Errorf("manifest has no name")
	}
	if m.HumanName == "" {
		m.HumanName = m.Name
	}

	settings, err := parseSettings(raw.Settings)
	if err != nil {
		return Manifest{}, err
	}
	m.Settings = settings

	if routes, ok := raw.Routes.(map[any]any); ok {
		if prefix, ok := routes["prefix"].(string); ok {
			m.RoutePrefix = prefix
		}
	}

	return m, nil
}

// parseSettings accepts both manifest shapes.
func parseSettings(raw any) ([]SettingDef, error) {
	switch t := raw.(type) {
	case nil:
		return nil, nil

	case []any:
		// List form: each entry carries its own key.
		out := make([]SettingDef, 0, len(t))
		for _, entry := range t {
			fields, ok := toStringMap(entry)
			if !ok {
				continue
			}
			def := settingFromFields(fields)
			if def.Key == "" {
				continue
			}
			out = append(out, def)
		}
		return out, nil

	case map[any]any:
		// Mapping form: the key is the setting name. YAML mappings have no
		// stable order, so sort for determinism.
		keys := make([]string, 0, len(t))
		byKey := map[string]map[string]any{}
		for k, v := range t {
			key, ok := k.(string)
			if !ok {
				continue
			}
			fields, ok := toStringMap(v)
			if !ok {
				fields = map[string]any{}
			}
			keys = append(keys, key)
			byKey[key] = fields
		}
		sort.Strings(keys)

		out := make([]SettingDef, 0, len(keys))
		for _, key := range keys {
			def := settingFromFields(byKey[key])
			def.Key = key
			out = append(out, def)
		}
		return out, nil

	default:
		return nil, fmt.Errorf("settings must be a mapping or a list, got %T", raw)
	}
}

func settingFromFields(fields map[string]any) SettingDef {
	def := SettingDef{
		Key:         stringField(fields, "key", "name"),
		Type:        stringField(fields, "type"),
		Label:       stringField(fields, "label", "displayName", "display_name"),
		Description: stringField(fields, "description", "help"),
		Default:     fields["default"],
	}
	if def.Type == "" {
		def.Type = "string"
	}
	if opts, ok := fields["options"].([]any); ok {
		def.Options = opts
	}
	return def
}

// LoadManifests scans a directory for plugin manifests.
//
// A malformed manifest is reported but does not stop the scan: one broken
// plugin must not hide every other.
func LoadManifests(dir string) ([]Manifest, []error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, []error{fmt.Errorf("read plugin directory: %w", err)}
	}

	var out []Manifest
	var errs []error

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(dir, entry.Name(), ManifestFile)
		if _, err := os.Stat(path); err != nil {
			continue
		}
		m, err := ParseManifestFile(path)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		out = append(out, m)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, errs
}

// ---------------------------------------------------------------- helpers ---

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

func firstNonNil(values ...any) any {
	for _, v := range values {
		if v != nil {
			return v
		}
	}
	return nil
}

// stringList normalises the several ways a list of strings can be written,
// dropping the placeholder null strings that leak in from stringified nulls.
func stringList(raw any) []string {
	var candidates []string

	switch t := raw.(type) {
	case nil:
		return nil
	case string:
		candidates = []string{t}
	case []any:
		for _, v := range t {
			if s, ok := v.(string); ok {
				candidates = append(candidates, s)
			}
		}
	case []string:
		candidates = t
	default:
		return nil
	}

	out := make([]string, 0, len(candidates))
	for _, c := range candidates {
		s := strings.TrimSpace(c)
		if s == "" || isNullToken(s) {
			continue
		}
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func isNullToken(s string) bool {
	switch strings.ToLower(s) {
	case "null", "none", "nil", "undefined":
		return true
	default:
		return false
	}
}

// toStringMap converts a YAML mapping, which decodes with `any` keys.
func toStringMap(v any) (map[string]any, bool) {
	switch t := v.(type) {
	case map[string]any:
		return t, true
	case map[any]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			if key, ok := k.(string); ok {
				out[key] = val
			}
		}
		return out, true
	default:
		return nil, false
	}
}

func stringField(fields map[string]any, names ...string) string {
	for _, name := range names {
		if v, ok := fields[name]; ok {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}
