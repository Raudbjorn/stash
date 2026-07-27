package aitag

import (
	"encoding/csv"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Per-tag marker rules.
//
// These come from CSV files, one per category, named for the category they
// configure. The format is the upstream server's, so an existing installation's
// tuned files can be dropped in unchanged - which matters, because these
// thresholds were fitted against real footage and inventing new ones would
// silently change every marker boundary.

// TagRule is the marker configuration for one label.
type TagRule struct {
	// OriginalTag is what the model emits.
	OriginalTag string
	// RenamedTag is what the marker is titled. Usually the original with an
	// "_AI" suffix, which is how a user tells generated markers from their own.
	RenamedTag string

	// Raw values, kept as written so "20s" and "5%" can be resolved against a
	// video's duration at use time rather than at parse time.
	MinMarkerDuration string
	MaxGap            string
	RequiredDuration  string
	TagThreshold      string
}

// CategoryRules maps a label to its rule, for one category.
type CategoryRules map[string]TagRule

// Rules is the whole configuration: category to labels.
type Rules struct {
	Categories map[string]CategoryRules
	// Defaults fill in a blank cell. The upstream config supplies these and
	// they are not the same as the hard-coded fallbacks.
	Defaults RuleDefaults
}

// RuleDefaults are the fallbacks for absent or blank values.
type RuleDefaults struct {
	MinMarkerDuration string
	MaxGap            string
	RequiredDuration  string
	TagThreshold      string
}

// DefaultRuleDefaults matches the shipped post_processing_config.yaml.
func DefaultRuleDefaults() RuleDefaults {
	return RuleDefaults{
		MinMarkerDuration: "12",
		MaxGap:            "6",
		RequiredDuration:  "20",
		TagThreshold:      "0.5",
	}
}

// NewRules returns an empty rule set with the shipped defaults.
func NewRules() *Rules {
	return &Rules{
		Categories: map[string]CategoryRules{},
		Defaults:   DefaultRuleDefaults(),
	}
}

// Lookup returns the rule for a label, and whether the category configures it.
//
// A label with no rule is NOT given defaults: the reference skips it entirely,
// because a category file is also the list of which labels are worth turning
// into markers. Treating an unlisted label as "use the defaults" would generate
// markers nobody asked for.
func (r *Rules) Lookup(category, tag string) (TagRule, bool) {
	rules, ok := r.Categories[category]
	if !ok {
		return TagRule{}, false
	}
	rule, ok := rules[tag]
	return rule, ok
}

// HasCategory reports whether a category is configured at all.
func (r *Rules) HasCategory(category string) bool {
	_, ok := r.Categories[category]
	return ok
}

// CategoryNames lists the configured categories in sorted order.
func (r *Rules) CategoryNames() []string {
	out := make([]string, 0, len(r.Categories))
	for name := range r.Categories {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// LoadRulesDir reads every .csv beneath dir, one category per file.
func LoadRulesDir(dir string) (*Rules, error) {
	rules := NewRules()

	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".csv") {
			return nil
		}

		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()

		category := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		parsed, err := ParseRulesCSV(file)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}

		// Merge rather than replace: a category split across several files is
		// unusual but harmless, and losing half of it silently would not be.
		existing, ok := rules.Categories[category]
		if !ok {
			rules.Categories[category] = parsed
			return nil
		}
		for tag, rule := range parsed {
			existing[tag] = rule
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return rules, nil
}

// ParseRulesCSV reads one category's rules.
func ParseRulesCSV(r io.Reader) (CategoryRules, error) {
	reader := csv.NewReader(r)
	// Tag names contain commas ("Ball Licking/Sucking" does not, but nothing
	// stops one), so field counts are not enforced; a short row is filled in
	// from the defaults at use time.
	reader.FieldsPerRecord = -1

	header, err := reader.Read()
	if err == io.EOF {
		return CategoryRules{}, nil
	}
	if err != nil {
		return nil, err
	}

	index := make(map[string]int, len(header))
	for i, name := range header {
		index[strings.TrimSpace(name)] = i
	}
	if _, ok := index["OriginalTag"]; !ok {
		return nil, fmt.Errorf("rules file has no OriginalTag column")
	}

	field := func(row []string, name string) string {
		i, ok := index[name]
		if !ok || i >= len(row) {
			return ""
		}
		return strings.TrimSpace(row[i])
	}

	out := CategoryRules{}
	for {
		row, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}

		original := field(row, "OriginalTag")
		if original == "" {
			continue
		}

		renamed := field(row, "RenamedTag")
		if renamed == "" {
			// A rule with no rename still generates markers; titling them with
			// the original name is better than dropping them.
			renamed = original
		}

		out[original] = TagRule{
			OriginalTag:       original,
			RenamedTag:        renamed,
			MinMarkerDuration: field(row, "MinMarkerDuration"),
			MaxGap:            field(row, "MaxGap"),
			RequiredDuration:  field(row, "RequiredDuration"),
			TagThreshold:      field(row, "TagThreshold"),
		}
	}
	return out, nil
}

// resolve returns a rule value, falling back to the defaults when blank.
//
// Blank is treated as absent, matching the reference's `if key in d and d[key]`
// - an empty CSV cell is falsy there, so it takes the default rather than
// parsing as zero.
func (r *Rules) resolve(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

// MinMarkerDuration resolves a rule's minimum marker length in seconds.
func (r *Rules) MinMarkerDuration(rule TagRule, videoDuration float64) float64 {
	return FormatDurationOrPercent(r.resolve(rule.MinMarkerDuration, r.Defaults.MinMarkerDuration), videoDuration)
}

// MaxGap resolves a rule's maximum joinable gap in seconds.
func (r *Rules) MaxGap(rule TagRule, videoDuration float64) float64 {
	return FormatDurationOrPercent(r.resolve(rule.MaxGap, r.Defaults.MaxGap), videoDuration)
}

// RequiredDuration resolves a rule's required total in seconds.
func (r *Rules) RequiredDuration(rule TagRule, videoDuration float64) float64 {
	return FormatDurationOrPercent(r.resolve(rule.RequiredDuration, r.Defaults.RequiredDuration), videoDuration)
}

// TagThreshold resolves a rule's confidence threshold.
func (r *Rules) TagThreshold(rule TagRule) float64 {
	value := r.resolve(rule.TagThreshold, r.Defaults.TagThreshold)
	parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil {
		return 0.5
	}
	return parsed
}

// FormatDurationOrPercent resolves a duration written as a number, "20s", or
// "5%" of the video's length.
//
// Ported including its error handling: an unparseable value yields 0, which the
// caller treats as "skip this tag" rather than "no minimum". That is the
// reference's behaviour and it is the safe direction - a malformed rule
// generates no markers instead of one per frame.
func FormatDurationOrPercent(value string, videoDuration float64) float64 {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return 0
	}

	switch {
	case strings.HasSuffix(trimmed, "%"):
		parsed, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(trimmed, "%")), 64)
		if err != nil {
			return 0
		}
		return parsed / 100 * videoDuration

	case strings.HasSuffix(trimmed, "s"), strings.HasSuffix(trimmed, "S"):
		parsed, err := strconv.ParseFloat(strings.TrimSpace(trimmed[:len(trimmed)-1]), 64)
		if err != nil {
			return 0
		}
		return parsed

	default:
		parsed, err := strconv.ParseFloat(trimmed, 64)
		if err != nil {
			return 0
		}
		return parsed
	}
}
