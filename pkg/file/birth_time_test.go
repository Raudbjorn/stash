package file

import (
	"os"
	"testing"
	"time"
)

func TestBirthTime(t *testing.T) {
	// Create a temp file
	f, err := os.CreateTemp("", "birth_time_test")
	if err != nil {
		t.Fatal(err)
	}
	name := f.Name()
	f.Close()
	defer os.Remove(name)

	info, err := os.Lstat(name)
	if err != nil {
		t.Fatal(err)
	}

	bt := BirthTime(info)

	if bt == nil {
		t.Skip("birth time not available on this platform")
	}

	if bt.IsZero() {
		t.Error("birth time should not be zero")
	}

	// Birth time should be close to now (we just created the file)
	now := time.Now()
	diff := now.Sub(*bt)
	if diff < 0 {
		diff = -diff
	}
	if diff > 5*time.Minute {
		t.Errorf("birth time %v is too far from now %v (diff: %v)", bt, now, diff)
	}
}
