package metadata

import (
	"testing"
	"time"
)

func d(y, m, day int) time.Time {
	return time.Date(y, time.Month(m), day, 0, 0, 0, 0, time.UTC)
}

func TestResolveDate(t *testing.T) {
	sanityBound := d(2030, 1, 1)

	tests := []struct {
		name           string
		signals        []DateSignal
		wantNil        bool
		wantDate       time.Time
		wantContested  bool
		wantConfidence float64
	}{
		{
			name:    "no signals",
			signals: nil,
			wantNil: true,
		},
		{
			name: "single EXIF signal",
			signals: []DateSignal{
				{Date: d(2020, 5, 1), Priority: DatePriorityExif, Source: "exif"},
			},
			wantDate:       d(2020, 5, 1),
			wantConfidence: 0.95,
		},
		{
			name: "EXIF corroborated by video creation time on same day",
			signals: []DateSignal{
				{Date: d(2020, 5, 1), Priority: DatePriorityExif, Source: "exif"},
				{Date: d(2020, 5, 1), Priority: DatePriorityVideoCreation, Source: "video"},
			},
			wantDate:       d(2020, 5, 1),
			wantConfidence: dateMaxConfidence,
		},
		{
			name: "EXIF contested by conflicting text date",
			signals: []DateSignal{
				{Date: d(2020, 5, 1), Priority: DatePriorityExif, Source: "exif"},
				{Date: d(2019, 1, 1), Priority: DatePriorityTextFull, Source: "text"},
			},
			wantDate:      d(2020, 5, 1),
			wantContested: true,
		},
		{
			name: "text date corroborated by year-only match",
			signals: []DateSignal{
				{Date: d(2020, 5, 1), Priority: DatePriorityTextFull, Source: "text"},
				{Date: d(2020, 1, 1), Priority: DatePriorityTextYearOnly, Source: "text-year"},
			},
			wantDate:       d(2020, 5, 1),
			wantConfidence: 0.65 + dateCorroborationBonus,
		},
		{
			name: "future date beyond sanity bound is discarded",
			signals: []DateSignal{
				{Date: d(2099, 1, 1), Priority: DatePriorityExif, Source: "exif"},
				{Date: d(2020, 5, 1), Priority: DatePriorityTextFull, Source: "text"},
			},
			wantDate:       d(2020, 5, 1),
			wantConfidence: 0.65,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveDate(tt.signals, sanityBound)
			if tt.wantNil {
				if got != nil {
					t.Fatalf("ResolveDate() = %#v, want nil", got)
				}
				return
			}

			if got == nil {
				t.Fatalf("ResolveDate() = nil, want non-nil")
			}
			if !got.Date.Equal(tt.wantDate) {
				t.Errorf("Date = %v, want %v", got.Date, tt.wantDate)
			}
			if got.Contested != tt.wantContested {
				t.Errorf("Contested = %v, want %v", got.Contested, tt.wantContested)
			}
			if tt.wantConfidence != 0 && got.Confidence != tt.wantConfidence {
				t.Errorf("Confidence = %v, want %v", got.Confidence, tt.wantConfidence)
			}
		})
	}
}
