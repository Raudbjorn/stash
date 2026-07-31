package metadata

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const MaxNFOSize int64 = 2 << 20

type NFOData struct {
	Path       string
	Title      string
	Performers []string
	Studio     string
	Date       string
	Group      string
	SceneIndex *int
}

type nfoDocument struct {
	Title         string `xml:"title"`
	OriginalTitle string `xml:"originaltitle"`
	Actors        []struct {
		Name string `xml:"name"`
	} `xml:"actor"`
	Performers     []string `xml:"performer"`
	Studio         string   `xml:"studio"`
	Premiered      string   `xml:"premiered"`
	ReleaseDate    string   `xml:"releasedate"`
	ProductionDate string   `xml:"production_date"`
	Date           string   `xml:"date"`
	Year           string   `xml:"year"`
	Movie          string   `xml:"movie"`
	Group          string   `xml:"group"`
	Set            struct {
		Name string `xml:"name"`
	} `xml:"set"`
	Scene       string `xml:"scene"`
	SceneNumber string `xml:"scene_number"`
	Index       string `xml:"index"`
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func parsePositiveInt(values ...string) *int {
	value := firstNonEmpty(values...)
	if value == "" {
		return nil
	}
	parsed, err := strconv.ParseInt(value, 10, 32)
	if err != nil || parsed <= 0 {
		return nil
	}
	ret := int(parsed)
	return &ret
}

func ParseNFO(reader io.Reader) (*NFOData, error) {
	limited := io.LimitReader(reader, MaxNFOSize+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("read NFO: %w", err)
	}
	if int64(len(data)) > MaxNFOSize {
		return nil, fmt.Errorf("NFO exceeds %d bytes", MaxNFOSize)
	}
	upper := bytes.ToUpper(data)
	if bytes.Contains(upper, []byte("<!DOCTYPE")) || bytes.Contains(upper, []byte("<!ENTITY")) {
		return nil, errors.New("NFO DTD and entity declarations are not allowed")
	}
	decoder := xml.NewDecoder(bytes.NewReader(data))
	decoder.Strict = true
	var document nfoDocument
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("parse NFO XML: %w", err)
	}

	ret := &NFOData{
		Title:      firstNonEmpty(document.Title, document.OriginalTitle),
		Studio:     firstNonEmpty(document.Studio),
		Date:       firstNonEmpty(document.Premiered, document.ReleaseDate, document.ProductionDate, document.Date, document.Year),
		Group:      firstNonEmpty(document.Movie, document.Group, document.Set.Name),
		SceneIndex: parsePositiveInt(document.Scene, document.SceneNumber, document.Index),
	}
	seenPerformers := make(map[string]struct{}, len(document.Actors)+len(document.Performers))
	for _, actor := range document.Actors {
		name := strings.TrimSpace(actor.Name)
		key := NormalizeKey(name)
		if key == "" {
			continue
		}
		if _, exists := seenPerformers[key]; !exists {
			seenPerformers[key] = struct{}{}
			ret.Performers = append(ret.Performers, name)
		}
	}
	for _, rawName := range document.Performers {
		name := strings.TrimSpace(rawName)
		key := NormalizeKey(name)
		if key == "" {
			continue
		}
		if _, exists := seenPerformers[key]; !exists {
			seenPerformers[key] = struct{}{}
			ret.Performers = append(ret.Performers, name)
		}
	}
	return ret, nil
}

var videoExtensions = map[string]struct{}{
	".3gp": {}, ".avi": {}, ".flv": {}, ".m2ts": {}, ".m4v": {}, ".mkv": {},
	".mov": {}, ".mp4": {}, ".mpeg": {}, ".mpg": {}, ".mts": {}, ".ts": {}, ".webm": {}, ".wmv": {},
}

func soleVideoInDirectory(videoPath string) (bool, error) {
	entries, err := os.ReadDir(filepath.Dir(videoPath))
	if err != nil {
		return false, err
	}
	videos := 0
	foundPrimary := false
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if _, ok := videoExtensions[strings.ToLower(filepath.Ext(entry.Name()))]; ok {
			if entry.Name() == filepath.Base(videoPath) {
				foundPrimary = true
			}
			videos++
			if videos > 1 {
				return false, nil
			}
		}
	}
	return videos == 1 && foundPrimary, nil
}

// ReadAdjacentNFO checks only the case-sensitive sidecar names allowed by the
// scene analyzer. Missing sidecars are a normal nil result.
func ReadAdjacentNFO(videoPath string) (*NFOData, error) {
	directory := filepath.Dir(videoPath)
	stem := strings.TrimSuffix(filepath.Base(videoPath), filepath.Ext(videoPath))
	candidates := []string{filepath.Join(directory, stem+".nfo")}
	if sole, err := soleVideoInDirectory(videoPath); err == nil && sole {
		candidates = append(candidates, filepath.Join(directory, "movie.nfo"))
	}
	for _, candidate := range candidates {
		file, err := os.Open(candidate)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("open NFO %s: %w", candidate, err)
		}
		parsed, parseErr := ParseNFO(file)
		closeErr := file.Close()
		if parseErr != nil {
			return nil, fmt.Errorf("%s: %w", candidate, parseErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("close NFO %s: %w", candidate, closeErr)
		}
		parsed.Path = candidate
		return parsed, nil
	}
	return nil, nil
}
