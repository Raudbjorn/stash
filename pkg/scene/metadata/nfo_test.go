package metadata

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/stashapp/stash/pkg/file"
	"github.com/stashapp/stash/pkg/models"
)

func TestReadNFOSelectionAndFields(t *testing.T) {
	dir := t.TempDir()
	videoPath := filepath.Join(dir, "scene.mp4")
	writeNFO := func(name, title string) {
		t.Helper()
		body := `<movie><title>` + title + `</title><premiered>2024-07-15</premiered><year>2024</year><studio>Example Studio</studio><set>Sample Movie</set><actor><name>Jane Doe</name></actor><actor><name>Kenzie Reeves</name></actor><actor><name>Jane Doe</name></actor></movie>`
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(videoPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	writeNFO("movie.nfo", "Movie Fallback")
	writeNFO("other.nfo", "Other Fallback")
	writeNFO("scene.nfo", "Exact Sidecar")
	video := &models.VideoFile{BaseFile: &models.BaseFile{Path: videoPath}}

	got, err := ReadNFO(&file.OsFS{}, video)
	if err != nil {
		t.Fatalf("ReadNFO() error = %v", err)
	}
	if got == nil {
		t.Fatal("ReadNFO() = nil")
	}
	if got.Title != "Exact Sidecar" {
		t.Fatalf("ReadNFO() = %#v, want exact-basename sidecar", got)
	}
	if got.Premiered != "2024-07-15" || got.Year != "2024" || got.Studio != "Example Studio" || got.SetName != "Sample Movie" {
		t.Errorf("structured fields not preserved: %#v", got)
	}
	if want := []string{"Jane Doe", "Kenzie Reeves"}; !reflect.DeepEqual(got.Actors, want) {
		t.Errorf("Actors = %#v, want %#v", got.Actors, want)
	}
}

func TestReadNFOFallbackOrder(t *testing.T) {
	dir := t.TempDir()
	videoPath := filepath.Join(dir, "scene.mp4")
	if err := os.WriteFile(videoPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		title string
	}{
		{name: "z-last.nfo", title: "Other"},
		{name: "movie.nfo", title: "Movie"},
	} {
		if err := os.WriteFile(filepath.Join(dir, tc.name), []byte(`<movie><title>`+tc.title+`</title></movie>`), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	video := &models.VideoFile{BaseFile: &models.BaseFile{Path: videoPath}}
	got, err := ReadNFO(&file.OsFS{}, video)
	if err != nil {
		t.Fatalf("ReadNFO() error = %v", err)
	}
	if got == nil || got.Title != "Movie" {
		t.Fatalf("ReadNFO() = %#v, want movie.nfo fallback", got)
	}
}
