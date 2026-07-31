// Package plugin handles AI plugin manifests, the remote catalog they are
// installed from, and the dependency planning that precedes an install.
//
// Everything here is pure Go and runs in the Stash process. Only the execution
// of plugin code needs a Python host; discovering, validating and planning does
// not, which is why those endpoints keep working even when the host is down.
package plugin

import (
	"strconv"
	"strings"
)

// Version compatibility. Plugins declare a `required_backend` expression such
// as ">=0.9.3" and refuse to load against an older server. The grammar is
// deliberately small - a space- or comma-separated list of comparator clauses,
// all of which must hold.

// devTokens mark a build that bypasses compatibility gates entirely.
//
// A developer running an unreleased server should not be blocked by a plugin's
// declared minimum, because the version string carries no useful ordering.
var devTokens = []string{"dev", "local", "snapshot", "dirty"}

// IsDevVersion reports whether a version string denotes a development build.
func IsDevVersion(v string) bool {
	s := strings.ToLower(strings.TrimSpace(v))
	if s == "" {
		return false
	}
	if strings.HasPrefix(s, "0.0.0") {
		return true
	}
	for _, token := range devTokens {
		if strings.Contains(s, token) {
			return true
		}
	}
	return false
}

// VersionSatisfies reports whether actual meets requirement.
//
// An empty requirement is satisfied by anything; an empty actual satisfies
// nothing (except that a dev build short-circuits to true before either check).
// A clause with no operator is an equality test.
func VersionSatisfies(actual, requirement string) bool {
	if strings.TrimSpace(requirement) == "" {
		return true
	}
	if strings.TrimSpace(actual) == "" {
		return false
	}
	if IsDevVersion(actual) {
		return true
	}

	current, ok := parseVersion(actual)
	if !ok {
		return false
	}

	// Commas and spaces are interchangeable separators.
	expr := strings.ReplaceAll(requirement, ",", " ")
	for _, clause := range strings.Fields(expr) {
		operator, target := splitClause(clause)
		if target == "" {
			continue
		}

		want, ok := parseVersion(target)
		if !ok {
			return false
		}

		cmp := compareVersions(current, want)
		switch operator {
		case "==", "=":
			if cmp != 0 {
				return false
			}
		case ">=":
			if cmp < 0 {
				return false
			}
		case ">":
			if cmp <= 0 {
				return false
			}
		case "<=":
			if cmp > 0 {
				return false
			}
		case "<":
			if cmp >= 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// splitClause separates a comparator from its target. A bare version is an
// equality test.
func splitClause(clause string) (operator, target string) {
	// Longest first, so ">=" is not read as ">".
	for _, candidate := range []string{">=", "<=", "==", ">", "<", "="} {
		if strings.HasPrefix(clause, candidate) {
			return candidate, strings.TrimSpace(clause[len(candidate):])
		}
	}
	return "==", strings.TrimSpace(clause)
}

// version is a dotted numeric version with an optional trailing suffix.
type version struct {
	parts  []int
	suffix string
}

// parseVersion accepts the shapes that actually appear: "1", "0.9.3",
// "1.2.3-beta". Anything with no leading number is rejected.
func parseVersion(s string) (version, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return version{}, false
	}
	s = strings.TrimPrefix(s, "v")

	// Split off a pre-release or build suffix.
	var suffix string
	if idx := strings.IndexAny(s, "-+"); idx >= 0 {
		suffix = s[idx+1:]
		s = s[:idx]
	}

	var parts []int
	for _, field := range strings.Split(s, ".") {
		if field == "" {
			continue
		}
		n, err := strconv.Atoi(field)
		if err != nil {
			return version{}, false
		}
		parts = append(parts, n)
	}
	if len(parts) == 0 {
		return version{}, false
	}
	return version{parts: parts, suffix: suffix}, true
}

// compareVersions orders two versions, treating missing components as zero so
// "1.2" and "1.2.0" compare equal.
//
// A version with a pre-release suffix sorts BEFORE the same version without
// one, matching the usual convention that 1.0.0-beta precedes 1.0.0.
func compareVersions(a, b version) int {
	maxLen := max(len(a.parts), len(b.parts))
	for i := 0; i < maxLen; i++ {
		av, bv := 0, 0
		if i < len(a.parts) {
			av = a.parts[i]
		}
		if i < len(b.parts) {
			bv = b.parts[i]
		}
		if av != bv {
			if av < bv {
				return -1
			}
			return 1
		}
	}

	switch {
	case a.suffix == b.suffix:
		return 0
	case a.suffix == "":
		return 1 // a release outranks a pre-release
	case b.suffix == "":
		return -1
	case a.suffix < b.suffix:
		return -1
	default:
		return 1
	}
}
