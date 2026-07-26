package metadata

import (
	"testing"
	"time"
)

func TestExtractDateSignals(t *testing.T) {
	tests := []struct {
		name      string
		text      string
		wantDate  time.Time
		wantPrio  int
		wantEmpty bool
	}{
		{
			name:     "ISO full date",
			text:     "Site - 2020-05-01 - Performer Name - Scene Title",
			wantDate: time.Date(2020, 5, 1, 0, 0, 0, 0, time.UTC),
			wantPrio: DatePriorityTextFull,
		},
		{
			name:     "2-digit year site convention",
			text:     "site.20.05.01.performer.name.title.mp4",
			wantDate: time.Date(2020, 5, 1, 0, 0, 0, 0, time.UTC),
			wantPrio: DatePriorityTextFull,
		},
		{
			name:     "compact 8-digit date",
			text:     "video_20200501_performer",
			wantDate: time.Date(2020, 5, 1, 0, 0, 0, 0, time.UTC),
			wantPrio: DatePriorityTextFull,
		},
		{
			name:     "year only fallback",
			text:     "Best Scenes of 2020 Compilation",
			wantDate: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
			wantPrio: DatePriorityTextYearOnly,
		},
		{
			name:      "resolution is not mistaken for a date",
			text:      "Scene Title 1080p 720p",
			wantEmpty: true,
		},
		{
			name:      "no date at all",
			text:      "Performer Name - Scene Title",
			wantEmpty: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractDateSignals(tt.text, "test")
			if tt.wantEmpty {
				if len(got) != 0 {
					t.Fatalf("ExtractDateSignals() = %#v, want empty", got)
				}
				return
			}

			if len(got) != 1 {
				t.Fatalf("ExtractDateSignals() = %#v, want exactly 1 signal", got)
			}
			if !got[0].Date.Equal(tt.wantDate) {
				t.Errorf("Date = %v, want %v", got[0].Date, tt.wantDate)
			}
			if got[0].Priority != tt.wantPrio {
				t.Errorf("Priority = %v, want %v", got[0].Priority, tt.wantPrio)
			}
		})
	}
}
