package metadata

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/stashapp/stash/pkg/models"
)

// NFO is the structured subset of Kodi sidecar metadata used by scene analysis.
type NFO struct {
	Title     string
	Premiered string
	Year      string
	Studio    string
	Actors    []string
	SetName   string
}

type nfoXML struct {
	Title     string     `xml:"title"`
	Premiered string     `xml:"premiered"`
	Year      string     `xml:"year"`
	Studio    string     `xml:"studio"`
	Actors    []nfoActor `xml:"actor"`
	Set       nfoSet     `xml:"set"`
}

type nfoActor struct {
	Name string `xml:"name"`
}

type nfoSet struct {
	Name string `xml:"name"`
	Text string `xml:",chardata"`
}

var commonVideoExtensions = map[string]struct{}{
	".3g2": {}, ".3gp": {}, ".asf": {}, ".avi": {}, ".divx": {},
	".flv": {}, ".m2ts": {}, ".m4v": {}, ".mkv": {}, ".mov": {},
	".mp4": {}, ".mpeg": {}, ".mpg": {}, ".mts": {}, ".ogv": {},
	".rm": {}, ".rmvb": {}, ".ts": {}, ".vob": {}, ".webm": {}, ".wmv": {},
}

// ReadNFO reads a same-basename sidecar, or an unambiguous movie.nfo fallback.
// A nil result with nil error means no eligible sidecar exists.
func ReadNFO(filesystem models.FS, video *models.VideoFile) (*NFO, error) {
	if filesystem == nil || video == nil || video.BaseFile == nil {
		return nil, nil
	}

	activeFS := filesystem
	var zipFS models.ZipFS
	if video.ZipFile != nil {
		var err error
		zipFS, err = filesystem.OpenZip(video.ZipFile.Base().Path, video.ZipFile.Base().Size)
		if err != nil {
			return nil, fmt.Errorf("opening sidecar archive: %w", err)
		}
		defer zipFS.Close()
		activeFS = zipFS
	}

	directory := filepath.Dir(video.Path)
	dir, err := activeFS.Open(directory)
	if err != nil {
		if isNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("opening sidecar directory %q: %w", directory, err)
	}
	entries, err := dir.ReadDir(-1)
	closeErr := dir.Close()
	if err != nil {
		return nil, fmt.Errorf("reading sidecar directory %q: %w", directory, err)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("closing sidecar directory %q: %w", directory, closeErr)
	}

	stem := strings.TrimSuffix(filepath.Base(video.Path), filepath.Ext(video.Path))
	var sameBasename, movieNFO string
	videoCount := 0
	primaryExtension := strings.ToLower(filepath.Ext(video.Path))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		extension := strings.ToLower(filepath.Ext(name))
		if extension == ".nfo" {
			entryStem := strings.TrimSuffix(name, filepath.Ext(name))
			switch {
			case strings.EqualFold(entryStem, stem):
				sameBasename = filepath.Join(directory, name)
			case strings.EqualFold(name, "movie.nfo"):
				movieNFO = filepath.Join(directory, name)
			}
		}
		if isRegularVideoEntry(entry, extension, primaryExtension) {
			videoCount++
		}
	}

	nfoPath := sameBasename
	if nfoPath == "" && movieNFO != "" && videoCount == 1 {
		nfoPath = movieNFO
	}
	if nfoPath == "" {
		return nil, nil
	}

	var reader io.ReadCloser
	if zipFS != nil {
		reader, err = zipFS.OpenOnly(nfoPath)
	} else {
		reader, err = filesystem.Open(nfoPath)
	}
	if err != nil {
		return nil, fmt.Errorf("opening NFO %q: %w", nfoPath, err)
	}
	defer reader.Close()

	var raw nfoXML
	if err := xml.NewDecoder(reader).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decoding NFO %q: %w", nfoPath, err)
	}

	ret := &NFO{
		Title:     strings.TrimSpace(raw.Title),
		Premiered: strings.TrimSpace(raw.Premiered),
		Year:      strings.TrimSpace(raw.Year),
		Studio:    strings.TrimSpace(raw.Studio),
		SetName:   strings.TrimSpace(raw.Set.Name),
	}
	if ret.SetName == "" {
		ret.SetName = strings.TrimSpace(raw.Set.Text)
	}
	seenActors := map[string]struct{}{}
	for _, actor := range raw.Actors {
		if name := strings.TrimSpace(actor.Name); name != "" {
			key := strings.ToLower(name)
			if _, seen := seenActors[key]; !seen {
				seenActors[key] = struct{}{}
				ret.Actors = append(ret.Actors, name)
			}
		}
	}
	return ret, nil
}

func isRegularVideoEntry(entry fs.DirEntry, extension, primaryExtension string) bool {
	if entry.Type()&fs.ModeType != 0 {
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return false
		}
	}
	if extension == primaryExtension {
		return true
	}
	_, ok := commonVideoExtensions[extension]
	return ok
}

func isNotExist(err error) bool {
	return err != nil && (errors.Is(err, fs.ErrNotExist) || strings.Contains(strings.ToLower(err.Error()), "not exist"))
}
