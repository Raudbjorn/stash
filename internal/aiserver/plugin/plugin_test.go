package plugin

import (
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// ------------------------------------------------------------- compat ---

func TestVersionSatisfies(t *testing.T) {
	cases := []struct {
		actual, requirement string
		want                bool
	}{
		// An empty requirement accepts anything.
		{"0.9.3", "", true},
		{"", "", true},
		// An empty version satisfies nothing.
		{"", ">=0.9.0", false},

		{"0.9.3", ">=0.9.3", true},
		{"0.9.3", ">=0.9.4", false},
		{"0.9.4", ">=0.9.3", true},
		{"0.9.3", ">0.9.3", false},
		{"0.9.4", ">0.9.3", true},
		{"0.9.3", "<=0.9.3", true},
		{"0.9.3", "<0.9.3", false},
		{"0.9.3", "==0.9.3", true},
		{"0.9.3", "=0.9.3", true},
		// A bare version is an equality test.
		{"0.9.3", "0.9.3", true},
		{"0.9.4", "0.9.3", false},

		// Multiple clauses must all hold; commas and spaces both separate.
		{"0.9.5", ">=0.9.0 <1.0.0", true},
		{"1.0.1", ">=0.9.0 <1.0.0", false},
		{"0.9.5", ">=0.9.0,<1.0.0", true},

		// Missing components are zero, so these compare equal.
		{"1.2", "1.2.0", true},
		{"1.2.0", "1.2", true},

		// A dev build bypasses the gate entirely.
		{"0.0.0", ">=99.0.0", true},
		{"1.0.0-dev", ">=99.0.0", true},
		{"2.0-snapshot", ">=99.0.0", true},
		{"1.0.0-dirty", ">=99.0.0", true},

		// Unparseable input fails closed rather than silently passing.
		{"not-a-version", ">=0.9.0", false},
		{"0.9.3", ">=not-a-version", false},
	}

	for _, tc := range cases {
		t.Run(tc.actual+" vs "+tc.requirement, func(t *testing.T) {
			if got := VersionSatisfies(tc.actual, tc.requirement); got != tc.want {
				t.Errorf("VersionSatisfies(%q, %q) = %v, want %v",
					tc.actual, tc.requirement, got, tc.want)
			}
		})
	}
}

// A pre-release sorts before the matching release.
func TestPrereleaseOrdering(t *testing.T) {
	if !VersionSatisfies("1.0.0", ">=1.0.0-beta") {
		t.Error("a release should satisfy >= its own pre-release")
	}
	if VersionSatisfies("1.0.0-beta", ">=1.0.0") {
		t.Error("a pre-release should not satisfy >= the release")
	}
}

func TestIsDevVersion(t *testing.T) {
	for _, v := range []string{"0.0.0", "0.0.0-anything", "1.0-dev", "2.0.0-local", "3.0-SNAPSHOT", "1.2.3-dirty"} {
		if !IsDevVersion(v) {
			t.Errorf("%q should be a dev version", v)
		}
	}
	for _, v := range []string{"", "0.9.3", "1.0.0", "1.0.0-beta"} {
		if IsDevVersion(v) {
			t.Errorf("%q should not be a dev version", v)
		}
	}
}

// ------------------------------------------------------------ manifest ---

func TestParseManifestFixtures(t *testing.T) {
	manifests, errs := LoadManifests("testdata")
	if len(errs) > 0 {
		t.Fatalf("errors loading fixtures: %v", errs)
	}
	if len(manifests) != 9 {
		t.Fatalf("loaded %d manifests, want the 9 fixtures", len(manifests))
	}

	byName := map[string]Manifest{}
	for _, m := range manifests {
		byName[m.Name] = m
	}

	base, ok := byName["base_test_plugin"]
	if !ok {
		t.Fatal("base_test_plugin missing")
	}
	if base.Version != "1.0.0" || base.RequiredBackend != ">=0.0.0" {
		t.Errorf("base = %+v", base)
	}
	if base.HumanName != "Base Test Plugin" {
		t.Errorf("human name = %q", base.HumanName)
	}
	if !reflect.DeepEqual(base.Files, []string{"plugin"}) {
		t.Errorf("files = %v", base.Files)
	}
	// depends_on: [] must yield no dependencies, not one empty string.
	if len(base.DependsOn) != 0 {
		t.Errorf("depends_on = %v, want empty", base.DependsOn)
	}
	// The mapping form of settings is keyed by name.
	if len(base.Settings) != 1 || base.Settings[0].Key != "test_setting" {
		t.Errorf("settings = %+v", base.Settings)
	}
	if base.Settings[0].Default != "test_value" {
		t.Errorf("default = %#v", base.Settings[0].Default)
	}

	deps := byName["test_dependencies"]
	if !reflect.DeepEqual(deps.DependsOn, []string{"base_test_plugin"}) {
		t.Errorf("depends_on = %v", deps.DependsOn)
	}

	missing := byName["test_missing_deps"]
	if len(missing.DependsOn) != 2 {
		t.Errorf("missing deps = %v", missing.DependsOn)
	}
}

// The list form of settings, as the official catalog uses.
func TestParseManifestListSettings(t *testing.T) {
	m, err := ParseManifest([]byte(`
name: skier_aitagging
version: 0.3.2
required_backend: '>=0.9.3'
files: [service]
settings:
  - key: server_url
    label: Remote Service URL
    type: string
    default: "http://localhost:8000"
    description: Base URL for the tagging backend
  - key: tagging_frame_interval
    label: Tagging Frame Interval
    type: number
    default: 2.0
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(m.Settings) != 2 {
		t.Fatalf("settings = %+v", m.Settings)
	}
	// List order is preserved, unlike the mapping form which is sorted.
	if m.Settings[0].Key != "server_url" || m.Settings[1].Key != "tagging_frame_interval" {
		t.Errorf("order = %v", m.Settings)
	}
	if m.Settings[0].Label != "Remote Service URL" || m.Settings[0].Type != "string" {
		t.Errorf("first setting = %+v", m.Settings[0])
	}
	if m.Settings[1].Default != 2.0 {
		t.Errorf("numeric default = %#v", m.Settings[1].Default)
	}
}

// Alias spellings accumulated across plugin generations must all resolve.
func TestParseManifestAliases(t *testing.T) {
	m, err := ParseManifest([]byte(`
name: aliased
version: 1.0.0
requiredBackend: '>=0.5.0'
title: Aliased Plugin
dependsOn: [other]
pip-dependencies: [numpy]
serverLink: https://example.invalid
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if m.RequiredBackend != ">=0.5.0" {
		t.Errorf("requiredBackend alias not read: %q", m.RequiredBackend)
	}
	if m.HumanName != "Aliased Plugin" {
		t.Errorf("title alias not read: %q", m.HumanName)
	}
	if !reflect.DeepEqual(m.DependsOn, []string{"other"}) {
		t.Errorf("dependsOn alias not read: %v", m.DependsOn)
	}
	if !reflect.DeepEqual(m.PipDependencies, []string{"numpy"}) {
		t.Errorf("pip-dependencies alias not read: %v", m.PipDependencies)
	}
	if m.ServerLink == "" {
		t.Error("serverLink alias not read")
	}
}

// Stringified nulls leak in from generated manifests and must not become
// dependency names.
func TestParseManifestDropsNullTokens(t *testing.T) {
	m, err := ParseManifest([]byte(`
name: nulls
version: 1.0.0
depends_on: ["null", "real_plugin", "None"]
pip_dependencies: ["nil"]
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !reflect.DeepEqual(m.DependsOn, []string{"real_plugin"}) {
		t.Errorf("depends_on = %v, want only the real one", m.DependsOn)
	}
	if m.PipDependencies != nil {
		t.Errorf("pip_dependencies = %v, want none", m.PipDependencies)
	}
}

func TestParseManifestRequiresName(t *testing.T) {
	if _, err := ParseManifest([]byte("version: 1.0.0\n")); err == nil {
		t.Error("a manifest with no name should be rejected")
	}
}

func TestParseManifestHumanNameDefaults(t *testing.T) {
	m, err := ParseManifest([]byte("name: bare\nversion: 1.0.0\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if m.HumanName != "bare" {
		t.Errorf("human name = %q, want the plugin name as a fallback", m.HumanName)
	}
}

func TestLoadManifestsMissingDirectory(t *testing.T) {
	manifests, errs := LoadManifests(filepath.Join("testdata", "does-not-exist"))
	if len(manifests) != 0 || len(errs) != 0 {
		t.Errorf("a missing plugin directory should be empty, not an error: %v %v", manifests, errs)
	}
}

// ---------------------------------------------------------------- plans ---

// fakeResolver drives planning from in-memory tables.
type fakeResolver struct {
	installed map[string]Manifest
	catalog   map[string]Available
}

func (f fakeResolver) Installed(name string) (Manifest, bool) {
	m, ok := f.installed[name]
	return m, ok
}

func (f fakeResolver) InstalledNames() []string {
	out := make([]string, 0, len(f.installed))
	for name := range f.installed {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func (f fakeResolver) Catalog(name string) (Available, bool) {
	a, ok := f.catalog[name]
	return a, ok
}

func TestPlanInstallOrdersDependenciesFirst(t *testing.T) {
	r := fakeResolver{
		installed: map[string]Manifest{},
		catalog: map[string]Available{
			"skier_aitagging":    {Name: "skier_aitagging", RequiredBackend: ">=0.9.3"},
			"personalized_tfidf": {Name: "personalized_tfidf", DependsOn: []string{"skier_aitagging"}, RequiredBackend: ">=0.9.2"},
		},
	}

	plan := PlanInstall(r, "personalized_tfidf", "0.9.3")
	if !plan.Executable() {
		t.Fatalf("plan not executable: %+v", plan)
	}
	// The dependency must come first, so the list can be executed in order.
	want := []string{"skier_aitagging", "personalized_tfidf"}
	if !reflect.DeepEqual(plan.Install, want) {
		t.Errorf("install order = %v, want %v", plan.Install, want)
	}
}

func TestPlanInstallReportsMissingDependencies(t *testing.T) {
	r := fakeResolver{
		installed: map[string]Manifest{},
		catalog: map[string]Available{
			"needs_missing": {Name: "needs_missing", DependsOn: []string{"nonexistent_plugin", "another_missing_plugin"}},
		},
	}

	plan := PlanInstall(r, "needs_missing", "0.9.3")
	if plan.Executable() {
		t.Error("a plan with missing dependencies should not be executable")
	}
	want := []string{"another_missing_plugin", "nonexistent_plugin"}
	if !reflect.DeepEqual(plan.Missing, want) {
		t.Errorf("missing = %v, want %v", plan.Missing, want)
	}
}

// The circular fixtures are the reason planning detects cycles rather than
// recursing forever.
func TestPlanInstallDetectsCycles(t *testing.T) {
	r := fakeResolver{
		installed: map[string]Manifest{},
		catalog: map[string]Available{
			"test_circular_deps_a": {Name: "test_circular_deps_a", DependsOn: []string{"test_circular_deps_b"}},
			"test_circular_deps_b": {Name: "test_circular_deps_b", DependsOn: []string{"test_circular_deps_a"}},
		},
	}

	plan := PlanInstall(r, "test_circular_deps_a", "0.9.3")
	if plan.Executable() {
		t.Error("a cyclic plan should not be executable")
	}
	if len(plan.Errors) == 0 || !strings.Contains(plan.Errors[0], "cycle") {
		t.Errorf("errors = %v, want a cycle report", plan.Errors)
	}
}

func TestPlanInstallVersionGate(t *testing.T) {
	r := fakeResolver{
		installed: map[string]Manifest{},
		catalog: map[string]Available{
			"future": {Name: "future", RequiredBackend: ">=99.0.0"},
		},
	}

	plan := PlanInstall(r, "future", "0.9.3")
	if plan.Executable() {
		t.Error("an incompatible plugin should not be installable")
	}
	if len(plan.Incompatible) != 1 || plan.Incompatible[0].Name != "future" {
		t.Errorf("incompatible = %+v", plan.Incompatible)
	}

	// A dev build bypasses the gate.
	if plan := PlanInstall(r, "future", "0.0.0-dev"); !plan.Executable() {
		t.Errorf("a dev build should bypass the version gate: %+v", plan)
	}
}

func TestPlanInstallSkipsSatisfiedDependencies(t *testing.T) {
	r := fakeResolver{
		installed: map[string]Manifest{
			"skier_aitagging": {Name: "skier_aitagging"},
		},
		catalog: map[string]Available{
			"personalized_tfidf": {Name: "personalized_tfidf", DependsOn: []string{"skier_aitagging"}},
		},
	}

	plan := PlanInstall(r, "personalized_tfidf", "0.9.3")
	if !reflect.DeepEqual(plan.Install, []string{"personalized_tfidf"}) {
		t.Errorf("install = %v, want only the requested plugin", plan.Install)
	}
}

func TestPlanInstallCollectsPipDependencies(t *testing.T) {
	r := fakeResolver{
		installed: map[string]Manifest{},
		catalog: map[string]Available{
			"a": {Name: "a", DependsOn: []string{"b"}, PipDependencies: []string{"numpy", "scipy"}},
			"b": {Name: "b", PipDependencies: []string{"numpy"}},
		},
	}

	plan := PlanInstall(r, "a", "0.9.3")
	// Deduplicated across the tree.
	if !reflect.DeepEqual(plan.PipDependencies, []string{"numpy", "scipy"}) {
		t.Errorf("pip dependencies = %v", plan.PipDependencies)
	}
}

// Removing a plugin takes its dependents with it: one left behind could not
// load anyway, and would fail confusingly later.
func TestPlanRemoveIncludesDependents(t *testing.T) {
	r := fakeResolver{
		installed: map[string]Manifest{
			"skier_aitagging":    {Name: "skier_aitagging"},
			"personalized_tfidf": {Name: "personalized_tfidf", DependsOn: []string{"skier_aitagging"}},
			"segment_similarity": {Name: "segment_similarity", DependsOn: []string{"skier_aitagging"}},
			"unrelated":          {Name: "unrelated"},
		},
	}

	plan := PlanRemove(r, "skier_aitagging")
	if len(plan.Errors) != 0 {
		t.Fatalf("errors: %v", plan.Errors)
	}

	want := []string{"personalized_tfidf", "segment_similarity"}
	if !reflect.DeepEqual(plan.Dependents, want) {
		t.Errorf("dependents = %v, want %v", plan.Dependents, want)
	}

	// Dependents must be removed before what they depend on.
	targetIdx := indexOf(plan.Remove, "skier_aitagging")
	for _, dep := range want {
		if idx := indexOf(plan.Remove, dep); idx < 0 || idx > targetIdx {
			t.Errorf("%s removed at %d, after its dependency at %d", dep, idx, targetIdx)
		}
	}
	if indexOf(plan.Remove, "unrelated") >= 0 {
		t.Error("an unrelated plugin was included in the removal")
	}
}

func TestPlanRemoveNotInstalled(t *testing.T) {
	r := fakeResolver{installed: map[string]Manifest{}}
	plan := PlanRemove(r, "ghost")
	if len(plan.Errors) == 0 {
		t.Error("removing an uninstalled plugin should report an error")
	}
}

// ------------------------------------------------------------ load order ---

func TestLoadOrderRespectsDependencies(t *testing.T) {
	manifests := []Manifest{
		{Name: "test_dependencies", DependsOn: []string{"base_test_plugin"}},
		{Name: "base_test_plugin"},
	}

	order, errs := LoadOrder(manifests)
	if len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if indexOf(order, "base_test_plugin") > indexOf(order, "test_dependencies") {
		t.Errorf("order = %v, want the dependency first", order)
	}
}

// A plugin caught in a cycle is excluded rather than loaded in an arbitrary
// order against a half-initialised peer.
func TestLoadOrderExcludesCycles(t *testing.T) {
	manifests := []Manifest{
		{Name: "test_circular_deps_a", DependsOn: []string{"test_circular_deps_b"}},
		{Name: "test_circular_deps_b", DependsOn: []string{"test_circular_deps_a"}},
		{Name: "healthy"},
	}

	order, errs := LoadOrder(manifests)
	if len(errs) == 0 {
		t.Fatal("a cycle should be reported")
	}
	if indexOf(order, "healthy") < 0 {
		t.Errorf("the healthy plugin was excluded: %v", order)
	}
	for _, name := range []string{"test_circular_deps_a", "test_circular_deps_b"} {
		if indexOf(order, name) >= 0 {
			t.Errorf("%s is in a cycle but was still ordered for loading", name)
		}
	}
}

func TestLoadOrderReportsMissingDependency(t *testing.T) {
	manifests := []Manifest{{Name: "test_missing_deps", DependsOn: []string{"nonexistent_plugin"}}}

	_, errs := LoadOrder(manifests)
	if len(errs) == 0 {
		t.Fatal("a missing dependency should be reported")
	}
	if !strings.Contains(errs[0].Error(), "nonexistent_plugin") {
		t.Errorf("error = %v", errs[0])
	}
}

// LoadOrder over the real fixtures: the dependency pair orders correctly and
// the circular pair is excluded.
func TestLoadOrderOverFixtures(t *testing.T) {
	manifests, errs := LoadManifests("testdata")
	if len(errs) > 0 {
		t.Fatalf("load: %v", errs)
	}

	order, orderErrs := LoadOrder(manifests)
	if len(orderErrs) == 0 {
		t.Error("the fixtures include a cycle and a missing dependency; both should be reported")
	}

	if indexOf(order, "base_test_plugin") > indexOf(order, "test_dependencies") {
		t.Errorf("dependency ordering wrong: %v", order)
	}
	for _, name := range []string{"test_circular_deps_a", "test_circular_deps_b"} {
		if indexOf(order, name) >= 0 {
			t.Errorf("%s should have been excluded as cyclic", name)
		}
	}
}

func indexOf(haystack []string, needle string) int {
	for i, v := range haystack {
		if v == needle {
			return i
		}
	}
	return -1
}
