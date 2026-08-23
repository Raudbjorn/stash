package metadata

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const (
	EntityLabelTechnical    = "technical metadata"
	EntityLabelDate         = "production date"
	EntityLabelPerformer    = "adult performer name"
	EntityLabelStudio       = "production studio"
	EntityLabelMovie        = "movie title"
	EntityLabelSceneMarker  = "scene marker"
	EntityLabelReleaseGroup = "video encoding group"
)

type SceneMarker struct {
	Index int
	Span  EntitySpan
}

type ParsedText struct {
	TechnicalSpans []EntitySpan
	DateSpans      []EntitySpan
	SceneMarker    *SceneMarker
	ReleaseGroup   *EntitySpan
}

var (
	technicalRE      = regexp.MustCompile(`(?i)(?:^|[^[:alnum:]])(2160p|1440p|1080p|720p|576p|480p|4k|8k|uhd|fhd|x264|x265|h[._-]?264|h[._-]?265|hevc|av1|vp9|xvid|divx|mp4|mkv|avi|wmv|mov|mpeg|mpg|webm|m4v|web[._-]?dl|webrip|blu[._-]?ray|bdrip|dvdrip|hdtv|remux|aac(?:2[._]0)?|ddp?[._]?[257][._]1|dts(?:[._-]?hd)?|truehd|atmos|hdr10\+?|hdr|dolby[._ -]?vision)(?:$|[^[:alnum:]])`)
	mediaExtensionRE = regexp.MustCompile(`(?i)\.(mkv|mp4|m4v|mov|avi|wmv|webm|mpg|mpeg|ts|m2ts|flv|3gp)$`)
	bracketRE        = regexp.MustCompile(`\[[^\[\]]*\]|\([^()]*\)`)
	squareBracketRE  = regexp.MustCompile(`\[[^\[\]]*\]`)
	sceneMarkerRE    = regexp.MustCompile(`(?i)(?:^|[^[:alnum:]])(scene|sc)[ ._-]*([0-9]{1,3})(?:$|[^[:alnum:]])`)
	fullDateRE       = regexp.MustCompile(`(?:^|[^0-9])([0-9]{2,4})[-._ ]([0-9]{1,2})[-._ ]([0-9]{1,2})(?:$|[^0-9])`)
	releaseNameRE    = regexp.MustCompile(`^[[:alnum:]][[:alnum:]_.-]{1,29}$`)
	spacedDashRE     = regexp.MustCompile(`\s+[-–—]+\s+`)
	emptyWrapperRE   = regexp.MustCompile(`\s*[\[\(]\s*[\]\)]\s*`)
	spaceRE          = regexp.MustCompile(`\s+`)
)

func captureSpan(text string, match []int, group int, label string) EntitySpan {
	start, end := match[group*2], match[group*2+1]
	return EntitySpan{ByteStart: start, ByteEnd: end, Text: text[start:end], Label: label, Score: 1}
}

func technicalSpans(text string) []EntitySpan {
	ret := make([]EntitySpan, 0, 8)
	for offset := 0; offset < len(text); {
		match := technicalRE.FindStringSubmatchIndex(text[offset:])
		if match == nil {
			break
		}
		start, end := offset+match[2], offset+match[3]
		ret = append(ret, EntitySpan{ByteStart: start, ByteEnd: end, Text: text[start:end], Label: EntityLabelTechnical, Score: 1})
		offset = end
	}
	if match := mediaExtensionRE.FindStringSubmatchIndex(text); match != nil {
		ret = append(ret, EntitySpan{ByteStart: match[0], ByteEnd: match[1], Text: text[match[0]:match[1]], Label: EntityLabelTechnical, Score: 1})
	}

	// If a bracket contains unknown words, preserve the whole bracket. Removing
	// only the technical words would imply that the remaining words were parsed.
	for _, bracket := range bracketRE.FindAllStringIndex(text, -1) {
		var inside []EntitySpan
		for _, span := range ret {
			if span.ByteStart >= bracket[0] && span.ByteEnd <= bracket[1] {
				inside = append(inside, span)
			}
		}
		if len(inside) == 0 {
			continue
		}
		residue := removeSpans(text[bracket[0]+1:bracket[1]-1], shiftSpans(inside, -(bracket[0]+1)))
		if strings.TrimFunc(residue, func(r rune) bool { return unicode.IsSpace(r) || strings.ContainsRune("._-+", r) }) == "" {
			filtered := ret[:0]
			for _, span := range ret {
				if span.ByteStart < bracket[0] || span.ByteEnd > bracket[1] {
					filtered = append(filtered, span)
				}
			}
			ret = append(filtered, EntitySpan{ByteStart: bracket[0], ByteEnd: bracket[1], Text: text[bracket[0]:bracket[1]], Label: EntityLabelTechnical, Score: 1})
		} else {
			filtered := ret[:0]
			for _, span := range ret {
				if span.ByteStart < bracket[0] || span.ByteEnd > bracket[1] {
					filtered = append(filtered, span)
				}
			}
			ret = filtered
		}
	}
	return ret
}

func shiftSpans(spans []EntitySpan, delta int) []EntitySpan {
	ret := make([]EntitySpan, len(spans))
	for i, span := range spans {
		span.ByteStart += delta
		span.ByteEnd += delta
		ret[i] = span
	}
	return ret
}

func validCalendarDate(year, month, day int) bool {
	if year < 1900 || year > 2100 || month < 1 || month > 12 || day < 1 || day > 31 {
		return false
	}
	date := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	return date.Year() == year && int(date.Month()) == month && date.Day() == day
}

func dateSpans(text string) []EntitySpan {
	var ret []EntitySpan
	for _, match := range fullDateRE.FindAllStringSubmatchIndex(text, -1) {
		year, _ := strconv.Atoi(text[match[2]:match[3]])
		month, _ := strconv.Atoi(text[match[4]:match[5]])
		day, _ := strconv.Atoi(text[match[6]:match[7]])
		year = normalizeYear(year)
		if validCalendarDate(year, month, day) {
			ret = append(ret, EntitySpan{ByteStart: match[2], ByteEnd: match[7], Text: text[match[2]:match[7]], Label: EntityLabelDate, Score: 1})
		}
	}
	return ret
}

func ParseTextMetadata(text string) ParsedText {
	parsed := ParsedText{TechnicalSpans: technicalSpans(text), DateSpans: dateSpans(text)}
	if match := sceneMarkerRE.FindStringSubmatchIndex(text); match != nil {
		index, _ := strconv.Atoi(text[match[4]:match[5]])
		if index > 0 {
			span := EntitySpan{ByteStart: match[2], ByteEnd: match[5], Text: text[match[2]:match[5]], Label: EntityLabelSceneMarker, Score: 1}
			parsed.SceneMarker = &SceneMarker{Index: index, Span: span}
		}
	}
	baseEnd := len(text)
	if extension := mediaExtensionRE.FindStringIndex(text); extension != nil {
		baseEnd = extension[0]
	}
	if dash := strings.LastIndex(text[:baseEnd], "-"); dash >= 0 {
		groupText := strings.TrimSpace(text[dash+1 : baseEnd])
		if len(groupText) >= 2 {
			switch groupText[0] {
			case '[':
				if groupText[len(groupText)-1] == ']' {
					groupText = strings.TrimSpace(groupText[1 : len(groupText)-1])
				}
			case '(':
				if groupText[len(groupText)-1] == ')' {
					groupText = strings.TrimSpace(groupText[1 : len(groupText)-1])
				}
			}
		}
		if releaseNameRE.MatchString(groupText) {
			technicalEndsAtDash := false
			for _, technical := range parsed.TechnicalSpans {
				if technical.ByteEnd == dash {
					technicalEndsAtDash = true
					break
				}
			}
			shortAlnum := len(groupText) >= 1 && len(groupText) <= 6
			if shortAlnum {
				for _, r := range groupText {
					if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
						shortAlnum = false
						break
					}
				}
			}
			if technicalEndsAtDash || shortAlnum {
				parsed.ReleaseGroup = &EntitySpan{
					ByteStart: dash, ByteEnd: baseEnd, Text: groupText,
					Label: EntityLabelReleaseGroup, Score: 1,
				}
			}
		}
	}
	return parsed
}

func (p ParsedText) RecognizedSpans() []EntitySpan {
	ret := make([]EntitySpan, 0, len(p.TechnicalSpans)+len(p.DateSpans)+2)
	ret = append(ret, p.TechnicalSpans...)
	ret = append(ret, p.DateSpans...)
	if p.SceneMarker != nil {
		ret = append(ret, p.SceneMarker.Span)
	}
	if p.ReleaseGroup != nil {
		ret = append(ret, *p.ReleaseGroup)
	}
	return ret
}

func removeSpans(text string, spans []EntitySpan) string {
	spans = append([]EntitySpan(nil), spans...)
	sort.Slice(spans, func(i, j int) bool {
		if spans[i].ByteStart != spans[j].ByteStart {
			return spans[i].ByteStart < spans[j].ByteStart
		}
		return spans[i].ByteEnd > spans[j].ByteEnd
	})
	var result strings.Builder
	cursor := 0
	for _, span := range spans {
		if span.ByteStart < cursor || span.ByteStart < 0 || span.ByteEnd > len(text) || span.ByteStart >= span.ByteEnd {
			continue
		}
		result.WriteString(text[cursor:span.ByteStart])
		result.WriteByte(' ')
		cursor = span.ByteEnd
	}
	result.WriteString(text[cursor:])
	return result.String()
}

// CleanTitle removes only supplied recognized entity spans plus deterministic
// technical/date/scene spans. Square-bracket groups are always dropped after
// span removal. Parentheses stay unless emptied or classified as a release group.
func CleanTitle(text string, recognized []EntitySpan) string {
	parsed := ParseTextMetadata(text)
	spans := append(parsed.RecognizedSpans(), recognized...)
	clean := removeSpans(text, spans)
	clean = squareBracketRE.ReplaceAllString(clean, " ")
	clean = strings.NewReplacer(".", " ", "_", " ").Replace(clean)
	clean = spacedDashRE.ReplaceAllString(clean, " ")
	clean = emptyWrapperRE.ReplaceAllString(clean, " ")
	clean = spaceRE.ReplaceAllString(clean, " ")
	return strings.Trim(clean, " \t\r\n-–—._")
}
