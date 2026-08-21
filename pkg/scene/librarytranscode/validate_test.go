package librarytranscode

import "testing"

func TestOutputOK(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		minSide int
		target  int
		srcDur  float64
		outDur  float64
		size    int64
		wantErr bool
	}{
		{name: "480p exact", minSide: 480, target: 480, srcDur: 10, outDur: 10, size: 100, wantErr: false},
		{name: "480p within 16", minSide: 464, target: 480, srcDur: 10, outDur: 10, size: 100, wantErr: false},
		{name: "480p too small", minSide: 400, target: 480, srcDur: 10, outDur: 10, size: 100, wantErr: true},
		{name: "360p exact", minSide: 360, target: 360, srcDur: 10, outDur: 10.4, size: 1, wantErr: false},
		{name: "zero size", minSide: 480, target: 480, srcDur: 10, outDur: 10, size: 0, wantErr: true},
		{name: "duration too far", minSide: 480, target: 480, srcDur: 100, outDur: 90, size: 100, wantErr: true},
		{name: "duration within 2 percent", minSide: 480, target: 480, srcDur: 100, outDur: 98.5, size: 100, wantErr: false},
		{name: "short clip 0.5s floor", minSide: 480, target: 480, srcDur: 2, outDur: 2.4, size: 100, wantErr: false},
		{name: "short clip exceeds 0.5s", minSide: 480, target: 480, srcDur: 2, outDur: 2.6, size: 100, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := outputOK(tt.minSide, tt.target, tt.srcDur, tt.outDur, tt.size)
			if (err != nil) != tt.wantErr {
				t.Fatalf("outputOK() err=%v wantErr=%v", err, tt.wantErr)
			}
		})
	}
}
