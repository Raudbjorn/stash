package plugin

import (
	"fmt"
	"sort"
	"strings"
)

// Install and removal planning.
//
// The plan is computed and shown to the user BEFORE anything is downloaded or
// deleted, because both operations pull in plugins the user did not name:
// installing one drags in its dependencies, and removing one strands everything
// that depended on it. Surfacing that first is the difference between an
// informed confirmation and a surprise.

// Available describes a plugin that could be installed, as advertised by a
// catalog.
type Available struct {
	Name            string
	HumanName       string
	Version         string
	Description     string
	RequiredBackend string
	DependsOn       []string
	PipDependencies []string
	// Source names the catalog it came from.
	Source string
	// Path is the subdirectory within the source repository.
	Path string
}

// InstallPlan is what installing a plugin would entail.
type InstallPlan struct {
	// Plugin is what the user asked for.
	Plugin string `json:"plugin"`
	// Install lists everything to be installed, dependencies first, so the
	// order is directly executable.
	Install []string `json:"install"`
	// AlreadyInstalled names requested plugins that are present and current.
	AlreadyInstalled []string `json:"already_installed"`
	// Missing names dependencies no catalog can supply. A non-empty Missing
	// makes the plan unexecutable.
	Missing []string `json:"missing"`
	// PipDependencies is the union of package specifiers to install.
	PipDependencies []string `json:"pip_dependencies"`
	// Incompatible names plugins whose required_backend this server fails.
	Incompatible []IncompatiblePlugin `json:"incompatible"`
	// Errors are structural problems, such as a dependency cycle.
	Errors []string `json:"errors"`
}

// Executable reports whether the plan can be carried out as-is.
func (p InstallPlan) Executable() bool {
	return len(p.Missing) == 0 && len(p.Errors) == 0 && len(p.Incompatible) == 0
}

// IncompatiblePlugin records a version gate failure.
type IncompatiblePlugin struct {
	Name            string `json:"name"`
	RequiredBackend string `json:"required_backend"`
	BackendVersion  string `json:"backend_version"`
}

// RemovePlan is what removing a plugin would entail.
type RemovePlan struct {
	Plugin string `json:"plugin"`
	// Remove lists everything to be removed, dependents first so nothing is
	// left referring to something already gone.
	Remove []string `json:"remove"`
	// Dependents names installed plugins that would break, which is why they
	// are included in Remove rather than silently orphaned.
	Dependents []string `json:"dependents"`
	Errors     []string `json:"errors"`
}

// Resolver supplies the information planning needs.
type Resolver interface {
	// Installed returns the manifest of an installed plugin.
	Installed(name string) (Manifest, bool)
	// InstalledNames lists every installed plugin.
	InstalledNames() []string
	// Catalog returns what a catalog can supply for a plugin name.
	Catalog(name string) (Available, bool)
}

// PlanInstall computes what installing a plugin would require.
//
// Dependencies are resolved depth-first so the returned order can be executed
// directly: a plugin never appears before something it depends on.
func PlanInstall(r Resolver, name, backendVersion string) InstallPlan {
	plan := InstallPlan{
		Plugin:           name,
		Install:          []string{},
		AlreadyInstalled: []string{},
		Missing:          []string{},
		PipDependencies:  []string{},
		Incompatible:     []IncompatiblePlugin{},
		Errors:           []string{},
	}

	// visiting tracks the current DFS path so a cycle is detected as a cycle
	// rather than as infinite recursion.
	visiting := map[string]bool{}
	done := map[string]bool{}
	pipSeen := map[string]bool{}

	var visit func(target string, path []string)
	visit = func(target string, path []string) {
		if done[target] {
			return
		}
		if visiting[target] {
			cycle := append(append([]string{}, path...), target)
			plan.Errors = append(plan.Errors,
				"dependency cycle: "+strings.Join(cycle, " -> "))
			return
		}

		visiting[target] = true
		defer func() {
			visiting[target] = false
			done[target] = true
		}()

		// An installed plugin satisfies the dependency; its own dependencies
		// are already met by virtue of it being installed.
		if manifest, installed := r.Installed(target); installed {
			if target != name {
				return
			}
			// The requested plugin being installed already is worth reporting,
			// but its dependency tree is still walked so a partially installed
			// set is completed.
			plan.AlreadyInstalled = append(plan.AlreadyInstalled, target)
			for _, dep := range manifest.DependsOn {
				visit(dep, append(path, target))
			}
			return
		}

		entry, found := r.Catalog(target)
		if !found {
			plan.Missing = append(plan.Missing, target)
			return
		}

		if !VersionSatisfies(backendVersion, entry.RequiredBackend) {
			plan.Incompatible = append(plan.Incompatible, IncompatiblePlugin{
				Name:            target,
				RequiredBackend: entry.RequiredBackend,
				BackendVersion:  backendVersion,
			})
			return
		}

		// Dependencies first, so the install order is executable.
		for _, dep := range entry.DependsOn {
			visit(dep, append(path, target))
		}

		for _, spec := range entry.PipDependencies {
			if !pipSeen[spec] {
				pipSeen[spec] = true
				plan.PipDependencies = append(plan.PipDependencies, spec)
			}
		}
		plan.Install = append(plan.Install, target)
	}

	visit(name, nil)

	sort.Strings(plan.Missing)
	sort.Strings(plan.PipDependencies)
	return plan
}

// PlanRemove computes what removing a plugin would require.
//
// Dependents are removed too rather than being left broken: a plugin whose
// dependency has vanished cannot load, so leaving it behind would only produce
// a confusing failure later.
func PlanRemove(r Resolver, name string) RemovePlan {
	plan := RemovePlan{
		Plugin:     name,
		Remove:     []string{},
		Dependents: []string{},
		Errors:     []string{},
	}

	if _, installed := r.Installed(name); !installed {
		plan.Errors = append(plan.Errors, "plugin is not installed: "+name)
		return plan
	}

	// Reverse dependency edges once, then walk them.
	dependents := map[string][]string{}
	for _, other := range r.InstalledNames() {
		manifest, ok := r.Installed(other)
		if !ok {
			continue
		}
		for _, dep := range manifest.DependsOn {
			dependents[dep] = append(dependents[dep], other)
		}
	}

	seen := map[string]bool{}
	var order []string

	var visit func(target string, path []string)
	visit = func(target string, path []string) {
		if seen[target] {
			return
		}
		for _, p := range path {
			if p == target {
				plan.Errors = append(plan.Errors,
					"dependency cycle: "+strings.Join(append(path, target), " -> "))
				return
			}
		}

		// Dependents first: nothing may outlive what it depends on.
		for _, dependent := range dependents[target] {
			visit(dependent, append(path, target))
		}

		if seen[target] {
			return
		}
		seen[target] = true
		order = append(order, target)
		if target != name {
			plan.Dependents = append(plan.Dependents, target)
		}
	}

	visit(name, nil)

	plan.Remove = order
	sort.Strings(plan.Dependents)
	return plan
}

// LoadOrder returns installed plugins in an order that satisfies their
// dependencies, reporting any cycle.
//
// A plugin in a cycle is excluded rather than loaded in an arbitrary order:
// loading it would run its registration against a half-initialised peer.
func LoadOrder(manifests []Manifest) ([]string, []error) {
	byName := make(map[string]Manifest, len(manifests))
	names := make([]string, 0, len(manifests))
	for _, m := range manifests {
		byName[m.Name] = m
		names = append(names, m.Name)
	}
	sort.Strings(names)

	var order []string
	var errs []error
	state := map[string]int{} // 0 unvisited, 1 visiting, 2 done

	var visit func(name string, path []string)
	visit = func(name string, path []string) {
		switch state[name] {
		case 2:
			return
		case 1:
			errs = append(errs, fmt.Errorf("dependency cycle: %s",
				strings.Join(append(path, name), " -> ")))
			return
		}

		manifest, ok := byName[name]
		if !ok {
			// A missing dependency is reported by the caller's own checks; here
			// it simply cannot be ordered.
			return
		}

		state[name] = 1
		for _, dep := range manifest.DependsOn {
			if _, present := byName[dep]; !present {
				errs = append(errs, fmt.Errorf("plugin %q depends on %q, which is not installed", name, dep))
				continue
			}
			visit(dep, append(path, name))
		}
		state[name] = 2
		order = append(order, name)
	}

	for _, name := range names {
		visit(name, nil)
	}

	// Anything caught in a cycle never reached state 2 cleanly; drop it.
	inCycle := map[string]bool{}
	for _, err := range errs {
		msg := err.Error()
		if !strings.HasPrefix(msg, "dependency cycle") {
			continue
		}
		for _, part := range strings.Split(strings.TrimPrefix(msg, "dependency cycle: "), " -> ") {
			inCycle[strings.TrimSpace(part)] = true
		}
	}
	if len(inCycle) > 0 {
		filtered := order[:0]
		for _, name := range order {
			if !inCycle[name] {
				filtered = append(filtered, name)
			}
		}
		order = filtered
	}

	return order, errs
}
