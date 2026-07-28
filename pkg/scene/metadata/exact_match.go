package metadata

// NamedAliases is the minimal library record used for exact entity linking.
type NamedAliases struct {
	ID      int
	Name    string
	Aliases []string
}

// ResolveExactNamed returns a library ID only when the normalized query
// identifies exactly one distinct record by name or alias. Duplicate aliases
// on the same record remain unambiguous; collisions across records do not.
func ResolveExactNamed(query string, records []NamedAliases) (int, bool) {
	key := NormalizeKey(query)
	if key == "" {
		return 0, false
	}
	matches := make(map[int]struct{}, 1)
	for _, record := range records {
		if NormalizeKey(record.Name) == key {
			matches[record.ID] = struct{}{}
		}
		for _, alias := range record.Aliases {
			if NormalizeKey(alias) == key {
				matches[record.ID] = struct{}{}
			}
		}
	}
	if len(matches) != 1 {
		return 0, false
	}
	for id := range matches {
		return id, true
	}
	return 0, false
}
