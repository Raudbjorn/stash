package generate

import (
	"reflect"
	"testing"
)

func TestParseSceneCutTimestamps(t *testing.T) {
	tests := []struct {
		name    string
		output  string
		want    []float64
		wantErr bool
	}{
		{
			name:   "no cuts",
			output: "",
			want:   []float64{},
		},
		{
			name: "single cut",
			output: "frame:42   pts:12345    pts_time:12.345\n" +
				"lavfi.scene_score=0.512340\n",
			want: []float64{12.345},
		},
		{
			name: "multiple cuts, out of order in output",
			output: "frame:100  pts:54321   pts_time:54.321\n" +
				"lavfi.scene_score=0.612340\n" +
				"frame:10   pts:1234    pts_time:1.234\n" +
				"lavfi.scene_score=0.512340\n",
			want: []float64{1.234, 54.321},
		},
		{
			name:    "malformed pts_time",
			output:  "frame:1 pts:1 pts_time:notanumber\n",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseSceneCutTimestamps([]byte(tt.output))
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseSceneCutTimestamps() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ParseSceneCutTimestamps() = %#v, want %#v", got, tt.want)
			}
		})
	}
}
