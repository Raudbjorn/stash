package metadata

import (
	"context"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// NormalizeKey returns the shared comparison key used by extraction and
// linking. It folds case and filename separators without dropping letters,
// apostrophes, or diacritics.
func NormalizeKey(value string) string {
	value = norm.NFKC.String(value)
	var b strings.Builder
	b.Grow(len(value))
	separator := false
	for _, r := range value {
		switch r {
		case '.', '_', '-', '‐', '‑', '–', '—':
			separator = true
		default:
			if separator && b.Len() > 0 {
				b.WriteByte(' ')
			}
			separator = false
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(cases.Fold().String(b.String())), " ")
}

// NormalizeSource fills the normalized text while preserving raw spelling.
func NormalizeSource(source Source) Source {
	source.RawText = strings.TrimSpace(source.RawText)
	source.Normalized = NormalizeKey(source.RawText)
	return source
}

func evidenceRank(kind EvidenceKind) int {
	switch kind {
	case EvidenceExplicitField:
		return 5
	case EvidenceExactLibraryMatch:
		return 4
	case EvidenceDeterministicSpan, EvidenceSceneMarker:
		return 3
	case EvidenceGLiNERSpan:
		return 2
	case EvidenceDateSignal, EvidenceTechnicalToken:
		return 1
	default:
		return 0
	}
}

func spanLength(span Span) int {
	if span.RuneEnd > span.RuneStart {
		return span.RuneEnd - span.RuneStart
	}
	return span.ByteEnd - span.ByteStart
}

func spansOverlap(a, b Span) bool {
	return a.ByteStart < b.ByteEnd && b.ByteStart < a.ByteEnd
}

// ResolveOverlappingSpans keeps the strongest non-overlapping evidence.
// Explicit fields and exact library matches outrank learned spans; otherwise
// confidence, then length, then source order decide deterministically.
func ResolveOverlappingSpans(spans []Span) []Span {
	ordered := append([]Span(nil), spans...)
	sort.SliceStable(ordered, func(i, j int) bool {
		left, right := ordered[i], ordered[j]
		if evidenceRank(left.Kind) != evidenceRank(right.Kind) {
			return evidenceRank(left.Kind) > evidenceRank(right.Kind)
		}
		if left.Score != right.Score {
			return left.Score > right.Score
		}
		if spanLength(left) != spanLength(right) {
			return spanLength(left) > spanLength(right)
		}
		if left.ByteStart != right.ByteStart {
			return left.ByteStart < right.ByteStart
		}
		return left.ByteEnd < right.ByteEnd
	})

	accepted := make([]Span, 0, len(ordered))
	for _, candidate := range ordered {
		overlaps := false
		for _, existing := range accepted {
			if candidate.Source.Kind == existing.Source.Kind && candidate.Source.Label == existing.Source.Label && spansOverlap(candidate, existing) {
				overlaps = true
				break
			}
		}
		if !overlaps {
			accepted = append(accepted, candidate)
		}
	}

	sort.SliceStable(accepted, func(i, j int) bool {
		if accepted[i].Source.Kind != accepted[j].Source.Kind {
			return accepted[i].Source.Kind < accepted[j].Source.Kind
		}
		if accepted[i].Source.Label != accepted[j].Source.Label {
			return accepted[i].Source.Label < accepted[j].Source.Label
		}
		return accepted[i].ByteStart < accepted[j].ByteStart
	})
	return accepted
}

// MergeCandidates deduplicates normalized values while retaining every source
// and accepted occurrence. The returned count is therefore a unique candidate
// count, not a token or evidence count.
func MergeCandidates(spans []Span) []Candidate {
	byKey := make(map[string]*Candidate, len(spans))
	entityIDs := make(map[string]int, len(spans))
	ambiguousIDs := make(map[string]bool)
	order := make([]string, 0, len(spans))
	for _, span := range spans {
		key := span.NormalizedKey
		if key == "" {
			key = NormalizeKey(span.Text)
		}
		if key == "" {
			continue
		}
		candidate := byKey[key]
		if candidate == nil {
			candidate = &Candidate{Value: span.Text, NormalizedKey: key}
			byKey[key] = candidate
			order = append(order, key)
		}
		candidate.Evidence = append(candidate.Evidence, span)
		candidate.OccurrenceCount++
		candidate.EvidenceCount = len(candidate.Evidence)
		if span.Score > candidate.Confidence {
			candidate.Confidence = span.Score
			candidate.Value = span.Text
		}
		if span.EntityID != nil {
			if existing, ok := entityIDs[key]; ok && existing != *span.EntityID {
				ambiguousIDs[key] = true
			} else {
				entityIDs[key] = *span.EntityID
			}
		}
		seenSource := false
		for _, source := range candidate.Sources {
			if source == span.Source.Kind {
				seenSource = true
				break
			}
		}
		if !seenSource {
			candidate.Sources = append(candidate.Sources, span.Source.Kind)
		}
	}

	ret := make([]Candidate, 0, len(order))
	for _, key := range order {
		candidate := byKey[key]
		if id, ok := entityIDs[key]; ok && !ambiguousIDs[key] {
			candidate.ExistingEntityID = &id
		}
		ret = append(ret, *candidate)
	}
	return ret
}

func makeSpan(source Source, entity EntitySpan, kind EvidenceKind) (Span, bool) {
	if entity.ByteStart < 0 || entity.ByteEnd > len(source.RawText) || entity.ByteStart >= entity.ByteEnd {
		return Span{}, false
	}
	text := entity.Text
	if text == "" {
		text = source.RawText[entity.ByteStart:entity.ByteEnd]
	}
	return Span{
		Source: source, ByteStart: entity.ByteStart, ByteEnd: entity.ByteEnd,
		RuneStart: utf8.RuneCountInString(source.RawText[:entity.ByteStart]),
		RuneEnd:   utf8.RuneCountInString(source.RawText[:entity.ByteEnd]),
		Text:      text, NormalizedKey: NormalizeKey(text), Label: entity.Label,
		Score: entity.Score, Kind: kind,
	}, true
}

func wholeSourceSpan(source Source, label string, kind EvidenceKind) Span {
	span, _ := makeSpan(source, EntitySpan{
		ByteStart: 0, ByteEnd: len(source.RawText), Text: source.RawText, Label: label, Score: 1,
	}, kind)
	return span
}

func sameSource(left, right Source) bool {
	return left.Kind == right.Kind && left.Label == right.Label && left.RawText == right.RawText
}

func analyze(ctx context.Context, inputs Inputs) (Analysis, error) {
	if err := ctx.Err(); err != nil {
		return Analysis{}, err
	}
	result := Analysis{Diagnostics: Diagnostics{Rejected: map[string]string{}}}
	result.Diagnostics.ModelAvailable = inputs.EntityExtractor != nil
	if inputs.EntityExtractor == nil {
		result.Diagnostics.ModelFallbackReason = "entity extractor unavailable"
	}

	type parsedSource struct {
		source Source
		parsed ParsedText
	}
	var (
		sources          []parsedSource
		performerSpans   []Span
		studioSpans      []Span
		movieSpans       []Span
		allEntitySpans   []Span
		dateSignals      = append([]DateSignal(nil), inputs.DateSignals...)
		modelSeen        = make(map[string]struct{}, len(inputs.Sources))
		structuredValues = make(map[string]struct{})
	)

	for _, rawSource := range inputs.Sources {
		if err := ctx.Err(); err != nil {
			return Analysis{}, err
		}
		source := NormalizeSource(rawSource)
		if source.Normalized == "" {
			continue
		}
		parsed := ParseTextMetadata(source.RawText)
		sources = append(sources, parsedSource{source: source, parsed: parsed})

		signals := ExtractDateSignals(source.RawText, source.Label)
		if source.Kind == SourceNFO && source.Label == "nfo production date" {
			for i := range signals {
				signals[i].Priority = DatePriorityExif
			}
		}
		dateSignals = append(dateSignals, signals...)

		switch {
		case source.Kind == SourceNFO && source.Label == "nfo performer":
			span := wholeSourceSpan(source, EntityLabelPerformer, EvidenceExplicitField)
			performerSpans = append(performerSpans, span)
			allEntitySpans = append(allEntitySpans, span)
			structuredValues[span.NormalizedKey] = struct{}{}
		case source.Kind == SourceNFO && source.Label == "nfo studio":
			span := wholeSourceSpan(source, EntityLabelStudio, EvidenceExplicitField)
			studioSpans = append(studioSpans, span)
			allEntitySpans = append(allEntitySpans, span)
			structuredValues[span.NormalizedKey] = struct{}{}
		case source.Kind == SourceNFO && source.Label == "nfo movie":
			span := wholeSourceSpan(source, EntityLabelMovie, EvidenceExplicitField)
			movieSpans = append(movieSpans, span)
			allEntitySpans = append(allEntitySpans, span)
			structuredValues[span.NormalizedKey] = struct{}{}
		case source.Label == "nfo title" || source.Kind == SourceContainerTitle:
			structuredValues[source.Normalized] = struct{}{}
		}

		if inputs.EntityExtractor == nil {
			continue
		}
		if _, duplicate := modelSeen[source.Normalized]; duplicate {
			continue
		}
		modelSeen[source.Normalized] = struct{}{}
		extracted, err := inputs.EntityExtractor.Extract(ctx, source.RawText)
		if err != nil {
			result.Diagnostics.ModelAvailable = false
			result.Diagnostics.ModelFallbackReason = "entity extraction failed"
			continue
		}
		for _, entity := range extracted {
			if inputs.EntityConfidenceThreshold > 0 && entity.Score < inputs.EntityConfidenceThreshold {
				continue
			}
			span, ok := makeSpan(source, entity, EvidenceGLiNERSpan)
			if !ok {
				continue
			}
			switch entity.Label {
			case EntityLabelPerformer:
				performerSpans = append(performerSpans, span)
				allEntitySpans = append(allEntitySpans, span)
			case EntityLabelStudio:
				studioSpans = append(studioSpans, span)
				allEntitySpans = append(allEntitySpans, span)
			case EntityLabelMovie:
				movieSpans = append(movieSpans, span)
				allEntitySpans = append(allEntitySpans, span)
			}
		}
	}

	for _, span := range inputs.AdditionalSpans {
		span.Source = NormalizeSource(span.Source)
		if span.NormalizedKey == "" {
			span.NormalizedKey = NormalizeKey(span.Text)
		}
		switch span.Label {
		case EntityLabelPerformer:
			performerSpans = append(performerSpans, span)
			allEntitySpans = append(allEntitySpans, span)
		case EntityLabelStudio:
			studioSpans = append(studioSpans, span)
			allEntitySpans = append(allEntitySpans, span)
		case EntityLabelMovie:
			movieSpans = append(movieSpans, span)
			allEntitySpans = append(allEntitySpans, span)
		}
	}

	if !result.Diagnostics.ModelAvailable {
		scorer := HeuristicNamePlausibilityScorer{}
		for _, parsed := range sources {
			source := parsed.source
			if source.Kind == SourceNFO || source.Kind == SourceContainerTag {
				continue
			}
			for _, candidate := range ExtractNameCandidates(source.RawText, func(value string) bool {
				_, excluded := structuredValues[NormalizeKey(value)]
				return excluded
			}) {
				if scorer.Score(candidate) < 0.3 {
					continue
				}
				start := strings.Index(source.RawText, candidate)
				if start < 0 {
					continue
				}
				span, _ := makeSpan(source, EntitySpan{
					ByteStart: start, ByteEnd: start + len(candidate), Text: candidate,
					Label: EntityLabelPerformer, Score: scorer.Score(candidate),
				}, EvidenceDeterministicSpan)
				performerSpans = append(performerSpans, span)
				allEntitySpans = append(allEntitySpans, span)
			}
		}
	}

	acceptedEntities := ResolveOverlappingSpans(allEntitySpans)
	exactIDs := make(map[string]int)
	ambiguousExactIDs := make(map[string]bool)
	for _, span := range allEntitySpans {
		if span.Kind != EvidenceExactLibraryMatch || span.EntityID == nil {
			continue
		}
		key := span.Label + "\x00" + span.NormalizedKey
		if existing, ok := exactIDs[key]; ok && existing != *span.EntityID {
			ambiguousExactIDs[key] = true
		} else {
			exactIDs[key] = *span.EntityID
		}
	}
	for index := range acceptedEntities {
		key := acceptedEntities[index].Label + "\x00" + acceptedEntities[index].NormalizedKey
		if id, ok := exactIDs[key]; ok && !ambiguousExactIDs[key] {
			acceptedEntities[index].EntityID = &id
		}
	}
	performerSpans = performerSpans[:0]
	studioSpans = studioSpans[:0]
	movieSpans = movieSpans[:0]
	for _, span := range acceptedEntities {
		switch span.Label {
		case EntityLabelPerformer:
			performerSpans = append(performerSpans, span)
		case EntityLabelStudio:
			studioSpans = append(studioSpans, span)
		case EntityLabelMovie:
			movieSpans = append(movieSpans, span)
		}
	}

	result.PerformerCandidates = MergeCandidates(performerSpans)
	result.UniquePotentialPerformers = len(result.PerformerCandidates)
	matched := make(map[int]struct{})
	for _, candidate := range result.PerformerCandidates {
		if candidate.ExistingEntityID != nil {
			matched[*candidate.ExistingEntityID] = struct{}{}
		}
	}
	for id := range matched {
		result.MatchedPerformerIDs = append(result.MatchedPerformerIDs, id)
	}
	sort.Ints(result.MatchedPerformerIDs)

	studioCandidates := MergeCandidates(studioSpans)
	if len(studioCandidates) == 1 {
		result.StudioCandidate = &studioCandidates[0]
		result.StudioID = studioCandidates[0].ExistingEntityID
	}

	sanityBound := inputs.SanityBound
	if sanityBound.IsZero() {
		sanityBound = time.Now()
	}
	result.Date = ResolveDate(dateSignals, sanityBound)

	var markerSource *parsedSource
	for i := range sources {
		if sources[i].parsed.SceneMarker == nil {
			continue
		}
		if markerSource == nil || sources[i].source.Trust > markerSource.source.Trust {
			markerSource = &sources[i]
		}
	}
	if markerSource != nil {
		groupSpans := movieSpans
		marker := markerSource.parsed.SceneMarker
		prefix := strings.Trim(markerSource.source.RawText[:marker.Span.ByteStart], " \t\r\n-–—._")
		if prefix != "" {
			prefixEnd := marker.Span.ByteStart
			for prefixEnd > 0 && strings.ContainsRune(" \t\r\n-–—._", rune(markerSource.source.RawText[prefixEnd-1])) {
				prefixEnd--
			}
			groupSpans = append(groupSpans, Span{
				Source: markerSource.source, ByteStart: 0, ByteEnd: prefixEnd,
				RuneStart: 0, RuneEnd: utf8.RuneCountInString(markerSource.source.RawText[:prefixEnd]),
				Text: prefix, NormalizedKey: NormalizeKey(prefix), Label: EntityLabelMovie,
				Score: 1, Kind: EvidenceSceneMarker,
			})
		}
		explicit := groupSpans[:0]
		for _, span := range groupSpans {
			if span.Kind == EvidenceExplicitField {
				explicit = append(explicit, span)
			}
		}
		if len(explicit) > 0 {
			groupSpans = explicit
		}
		if len(explicit) == 0 {
			exact := groupSpans[:0]
			for _, span := range groupSpans {
				if span.Kind == EvidenceExactLibraryMatch && span.EntityID != nil {
					exact = append(exact, span)
				}
			}
			if len(exact) > 0 {
				groupSpans = exact
			}
		}
		groupCandidates := MergeCandidates(groupSpans)
		if len(groupCandidates) == 1 {
			index := marker.Index
			result.Group = &GroupProposal{
				Name: groupCandidates[0].Value, ExistingID: groupCandidates[0].ExistingEntityID,
				SceneIndex: &index, Evidence: groupCandidates[0].Evidence,
			}
		}
	}

	var titleSource *Source
	for i := range sources {
		source := &sources[i].source
		if source.Label == "nfo title" {
			titleSource = source
			break
		}
		if titleSource == nil && source.Kind == SourceContainerTitle {
			titleSource = source
		}
	}
	if titleSource == nil {
		for i := range sources {
			if sources[i].source.Kind == SourceFilename {
				titleSource = &sources[i].source
				break
			}
		}
	}
	if titleSource == nil {
		for i := range sources {
			if sources[i].source.Kind == SourceSceneTitle {
				titleSource = &sources[i].source
				break
			}
		}
	}
	if titleSource != nil {
		var recognized []EntitySpan
		for _, span := range acceptedEntities {
			if sameSource(span.Source, *titleSource) && span.Kind != EvidenceDeterministicSpan &&
				(span.Label == EntityLabelPerformer || span.Label == EntityLabelStudio) {
				recognized = append(recognized, EntitySpan{
					ByteStart: span.ByteStart, ByteEnd: span.ByteEnd, Text: span.Text, Label: span.Label, Score: span.Score,
				})
			}
		}
		if result.Group != nil {
			for _, span := range result.Group.Evidence {
				if sameSource(span.Source, *titleSource) {
					recognized = append(recognized, EntitySpan{
						ByteStart: span.ByteStart, ByteEnd: span.ByteEnd, Text: span.Text, Label: span.Label, Score: span.Score,
					})
				}
			}
		}
		if clean := CleanTitle(titleSource.RawText, recognized); clean != "" {
			result.Title = &TitleProposal{Value: clean, Source: *titleSource}
		}
	}

	for _, parsed := range sources {
		if parsed.source.Kind != SourceFilename {
			continue
		}
		result.Technical.CleanedTitle = CleanTitle(parsed.source.RawText, nil)
		if parsed.parsed.ReleaseGroup != nil {
			result.Technical.EncodingGroup = parsed.parsed.ReleaseGroup.Text
		}
		if parsed.parsed.SceneMarker != nil {
			index := parsed.parsed.SceneMarker.Index
			result.Technical.SceneMarker = parsed.parsed.SceneMarker.Span.Text
			result.Technical.SceneIndex = &index
		}
		if result.Group != nil {
			result.Technical.GroupCandidate = result.Group.Name
		}
		for _, entity := range parsed.parsed.RecognizedSpans() {
			span, ok := makeSpan(parsed.source, entity, EvidenceTechnicalToken)
			if ok {
				reason := EvidenceTechnicalToken
				if entity.Label == EntityLabelDate {
					reason = EvidenceDateSignal
				} else if entity.Label == EntityLabelSceneMarker {
					reason = EvidenceSceneMarker
				}
				result.Technical.RemovedTokens = append(result.Technical.RemovedTokens, RemovedToken{Text: entity.Text, Reason: reason, Span: span})
			}
		}
		break
	}

	result.Diagnostics.RawPerformerSpanCount = len(performerSpans)
	result.Diagnostics.UniquePerformerCount = result.UniquePotentialPerformers
	result.Diagnostics.ExactLibraryMatches = len(result.MatchedPerformerIDs)
	return result, nil
}
