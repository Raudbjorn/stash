package metadata

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ReleaseName is the deterministic interpretation of a release-style name.
type ReleaseName struct {
	Title        string
	ReleaseGroup string
	MovieTitle   string
	SceneNumber  *int
	Confidence   float64
	Evidence     []string
}

var (
	technicalPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\b(?:[1-9]\d{2,3}p|[248]k|uhd)\b`),
		regexp.MustCompile(`(?i)\b(?:x26[45]|h\.?26[45]|hevc|av1|xvid|divx|vp9)\b`),
		regexp.MustCompile(`(?i)\b(?:web[- .]?dl|webrip|bluray|bdrip|dvdrip|hdtv)\b`),
		regexp.MustCompile(`(?i)\b(?:hdr10\+?|dolby[ .]?vision|dv|aac(?:2\.0|5\.1)?|dts(?:-hd)?|truehd|atmos|10bit)\b`),
		regexp.MustCompile(`(?i)\b(?:19|20)\d{2}[-._ ]\d{1,2}[-._ ]\d{1,2}\b`),
		regexp.MustCompile(`(?i)\b(?:19|20)\d{6}\b`),
	}
	releaseGroupPattern = regexp.MustCompile(`-([\pL\pN][\pL\pN._]{1,15})\s*$`)
	sceneMarkerPattern  = regexp.MustCompile(`(?i)(?:^|[\s._-])(?:scene|sc)[\s._#-]*(\d{1,4})(?:$|[\s._-])`)
)

// ParseReleaseName removes recognized entities and technical release tokens
// while preserving source capitalization.
func ParseReleaseName(text string, spans []EntitySpan) ReleaseName {
	ret := ReleaseName{Confidence: 0.75}
	masked := []byte(text)
	for _, span := range spans {
		if span.Source.Kind != SourceFilename || span.Start < 0 || span.End > len(masked) || span.Start >= span.End {
			continue
		}
		switch span.Kind {
		case EntityPerformer, EntityProductionStudio, EntityProductionDate:
			blankBytes(masked, span.Start, span.End)
		case EntitySceneTitle:
			if span.Confidence >= ret.Confidence {
				ret.Title = span.Text
				ret.Confidence = span.Confidence
				ret.Evidence = append(ret.Evidence, "gliner:scene_title")
			}
		case EntityMovieTitle:
			ret.MovieTitle = span.Text
			ret.Evidence = append(ret.Evidence, "gliner:movie_title")
		}
	}

	working := normalizeReleaseSeparators(string(masked))
	hadTechnical := false
	for _, pattern := range technicalPatterns {
		for _, location := range pattern.FindAllStringIndex(working, -1) {
			hadTechnical = true
			working = blankStringRange(working, location[0], location[1])
		}
	}
	if hadTechnical {
		ret.Evidence = append(ret.Evidence, "technical_tokens")
	}

	if location := releaseGroupPattern.FindStringSubmatchIndex(text); location != nil {
		groupStart, groupEnd := location[2], location[3]
		modelConfirmed := false
		for _, span := range spans {
			if span.Kind == EntityReleaseGroup && span.Confidence >= 0.8 && span.Start == groupStart && span.End == groupEnd {
				modelConfirmed = true
				break
			}
		}
		if hadTechnical || modelConfirmed {
			ret.ReleaseGroup = text[groupStart:groupEnd]
			ret.Evidence = append(ret.Evidence, "release_group")
			if normalizedGroup := normalizeReleaseSeparators(text[location[0]:location[1]]); normalizedGroup != "" {
				working = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(working), strings.TrimSpace(normalizedGroup)))
			}
		}
	}

	if location := sceneMarkerPattern.FindStringSubmatchIndex(working); location != nil {
		number, err := strconv.Atoi(working[location[2]:location[3]])
		if err == nil {
			ret.SceneNumber = &number
			prefix := strings.TrimSpace(working[:location[0]])
			if ret.MovieTitle == "" {
				ret.MovieTitle = prefix
			}
			working = strings.TrimSpace(working[:location[0]] + " " + working[location[1]:])
			ret.Evidence = append(ret.Evidence, "scene_number")
		}
	}

	working = strings.Trim(collapseReleaseWhitespace(working), " -._")
	if ret.Title == "" {
		ret.Title = working
	}
	if !hasTwoLetters(ret.Title) {
		ret.Title = ""
		ret.Confidence = 0
	}
	return ret
}

func blankBytes(value []byte, start, end int) {
	for i := start; i < end; i++ {
		value[i] = ' '
	}
}

func blankStringRange(value string, start, end int) string {
	bytes := []byte(value)
	blankBytes(bytes, start, end)
	return string(bytes)
}

func normalizeReleaseSeparators(value string) string {
	var builder strings.Builder
	spacePending := false
	for _, r := range value {
		if r == '.' || r == '_' || unicode.IsSpace(r) {
			spacePending = builder.Len() > 0
			continue
		}
		if spacePending {
			builder.WriteByte(' ')
			spacePending = false
		}
		builder.WriteRune(r)
	}
	return strings.TrimSpace(builder.String())
}

func collapseReleaseWhitespace(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func hasTwoLetters(value string) bool {
	letters := 0
	for len(value) > 0 {
		r, size := utf8.DecodeRuneInString(value)
		value = value[size:]
		if unicode.IsLetter(r) {
			letters++
			if letters >= 2 {
				return true
			}
		}
	}
	return false
}
