package metadata

import (
	"testing"
)

func TestParseReleaseName(t *testing.T) {
	tests := []struct {
		name            string
		text            string
		spans           []EntitySpan
		wantTitle       string
		wantMovie       string
		wantSceneNumber int
		wantGroup       string
	}{
		{
			name: "technical suffix and group",
			text: "Sample.Movie.Scene.2.2024.07.15.1080p.WEB-DL.x264-GROUP",
			spans: []EntitySpan{{
				Text: "2024.07.15", Kind: EntityProductionDate, Start: 21, End: 31,
				Source: TextSource{Kind: SourceFilename},
			}},
			wantTitle:       "Sample Movie",
			wantMovie:       "Sample Movie",
			wantSceneNumber: 2,
			wantGroup:       "GROUP",
		},
		{
			name: "remove performer and studio spans",
			text: "Example Studio - Jane Doe - Canonical Scene Title - 2160p",
			spans: []EntitySpan{
				{Text: "Example Studio", Kind: EntityProductionStudio, Start: 0, End: 14, Source: TextSource{Kind: SourceFilename}},
				{Text: "Jane Doe", Kind: EntityPerformer, Start: 17, End: 25, Source: TextSource{Kind: SourceFilename}},
			},
			wantTitle: "Canonical Scene Title",
		},
		{
			name: "short nonsense is rejected",
			text: "1080p x264",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseReleaseName(tt.text, tt.spans)
			if got.Title != tt.wantTitle || got.MovieTitle != tt.wantMovie || got.ReleaseGroup != tt.wantGroup {
				t.Fatalf("ParseReleaseName() = %#v", got)
			}
			if tt.wantSceneNumber == 0 {
				if got.SceneNumber != nil {
					t.Fatalf("SceneNumber = %v, want nil", *got.SceneNumber)
				}
			} else if got.SceneNumber == nil || *got.SceneNumber != tt.wantSceneNumber {
				t.Fatalf("SceneNumber = %v, want %d", got.SceneNumber, tt.wantSceneNumber)
			}
		})
	}
}
