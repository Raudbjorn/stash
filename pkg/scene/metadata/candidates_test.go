package metadata

import (
	"reflect"
	"testing"
)

func TestExtractNameCandidates(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		exclude func(string) bool
		want    []string
	}{
		{
			name: "simple name pair",
			text: "Jane Doe - First Scene",
			want: []string{"Jane Doe"},
		},
		{
			name:    "filename with delimiters, studio name excluded",
			text:    "Studio Name - Jane Doe - Scene Title 1080p",
			exclude: func(c string) bool { return c == "studio name" },
			want:    []string{"Jane Doe"},
		},
		{
			name: "stop word breaks pair",
			text: "The Best Scene",
			want: nil,
		},
		{
			name:    "excluded studio name",
			text:    "Studio Site - Jane Doe",
			exclude: func(c string) bool { return c == "studio site" },
			want:    []string{"Jane Doe"},
		},
		{
			name: "all lowercase is not name-like",
			text: "jane doe scene",
			want: nil,
		},
		{
			name: "duplicate pairs deduped",
			text: "Jane Doe and Jane Doe again",
			want: []string{"Jane Doe"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractNameCandidates(tt.text, tt.exclude)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ExtractNameCandidates() = %#v, want %#v", got, tt.want)
			}
		})
	}
}
