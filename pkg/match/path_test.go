package match

import "testing"

func Test_nameMatchesPath(t *testing.T) {
	const name = "first last"
	const unicodeName = "伏字"

	tests := []struct {
		testName string
		name     string
		path     string
		want     int
	}{
		{
			"exact",
			name,
			name,
			0,
		},
		{
			"partial",
			name,
			"first",
			-1,
		},
		{
			"separator",
			name,
			"first.last",
			0,
		},
		{
			"separator",
			name,
			"first-last",
			0,
		},
		{
			"separator",
			name,
			"first_last",
			0,
		},
		{
			"separators",
			name,
			"first.-_ last",
			0,
		},
		{
			"within string",
			name,
			"before_first last/after",
			6,
		},
		{
			"within string case insensitive",
			name,
			"before FIRST last/after",
			6,
		},
		{
			"not within string",
			name,
			"beforefirst last/after",
			-1,
		},
		{
			"not within string",
			name,
			"before/first lastafter",
			-1,
		},
		{
			"not within string",
			name,
			"first last1",
			-1,
		},
		{
			"not within string",
			name,
			"1first last",
			-1,
		},
		{
			"unicode",
			unicodeName,
			unicodeName,
			0,
		},
	}
	// run each case with no cache and with a shared cache to exercise both the
	// direct-compile and memoised paths (results must be identical).
	cache := &Cache{}
	for _, tt := range tests {
		t.Run(tt.testName, func(t *testing.T) {
			if got := nameMatchesPath(nil, tt.name, tt.path); got != tt.want {
				t.Errorf("nameMatchesPath(nil) = %v, want %v", got, tt.want)
			}
			if got := nameMatchesPath(cache, tt.name, tt.path); got != tt.want {
				t.Errorf("nameMatchesPath(cache) = %v, want %v", got, tt.want)
			}
			// second cached lookup must be consistent
			if got := nameMatchesPath(cache, tt.name, tt.path); got != tt.want {
				t.Errorf("nameMatchesPath(cache, repeat) = %v, want %v", got, tt.want)
			}
		})
	}
}
