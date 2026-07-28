package metadata

import (
	"context"
	"time"
)

// SourceKind identifies where metadata text originated.
type SourceKind string

const (
	SourceSceneTitle       SourceKind = "scene_title"
	SourceFilename         SourceKind = "filename"
	SourceNFO              SourceKind = "nfo"
	SourceContainerTitle   SourceKind = "container_title"
	SourceContainerComment SourceKind = "container_comment"
	SourceContainerTag     SourceKind = "container_tag"
	SourceDetails          SourceKind = "details"
)

// TrustTier orders source reliability. Larger values are more trustworthy.
type TrustTier uint8

const (
	TrustLearned TrustTier = iota + 1
	TrustFreeText
	TrustContainer
	TrustExplicit
)

// Source is one bounded text input and its provenance.
type Source struct {
	Kind       SourceKind
	Label      string
	RawText    string
	Normalized string
	Trust      TrustTier
}

// EvidenceKind identifies how a span or candidate was obtained.
type EvidenceKind string

const (
	EvidenceDeterministicSpan EvidenceKind = "deterministic_span"
	EvidenceExplicitField     EvidenceKind = "explicit_structured_field"
	EvidenceExactLibraryMatch EvidenceKind = "exact_library_match"
	EvidenceGLiNERSpan        EvidenceKind = "gliner_span"
	EvidenceTechnicalToken    EvidenceKind = "technical_token"
	EvidenceDateSignal        EvidenceKind = "date_signal"
	EvidenceSceneMarker       EvidenceKind = "scene_marker"
)

// Span is a half-open range in a source. Byte and rune offsets are both kept
// so callers never have to guess how a model's Unicode offsets map to Go text.
type Span struct {
	Source        Source
	ByteStart     int
	ByteEnd       int
	RuneStart     int
	RuneEnd       int
	Text          string
	NormalizedKey string
	Label         string
	Score         float64
	Kind          EvidenceKind
	EntityID      *int
}

// Candidate is a normalized entity proposal with all supporting evidence.
type Candidate struct {
	Value            string
	NormalizedKey    string
	Evidence         []Span
	Sources          []SourceKind
	OccurrenceCount  int
	EvidenceCount    int
	Confidence       float64
	ExistingEntityID *int
}

// RemovedToken records title text removed by an accepted parser decision.
type RemovedToken struct {
	Text   string
	Reason EvidenceKind
	Span   Span
}

// TechnicalMetadata is the deterministic interpretation of one title-like
// source.
type TechnicalMetadata struct {
	CleanedTitle   string
	RemovedTokens  []RemovedToken
	EncodingGroup  string
	SceneMarker    string
	SceneIndex     *int
	GroupCandidate string
}

// TitleProposal carries both the proposed value and its provenance.
type TitleProposal struct {
	Value  string
	Source Source
}

// GroupProposal describes an existing movie/group relationship proposal.
type GroupProposal struct {
	Name       string
	ExistingID *int
	SceneIndex *int
	Evidence   []Span
}

// Diagnostics contains bounded analysis facts suitable for structured logs.
type Diagnostics struct {
	RawPerformerSpanCount int
	UniquePerformerCount  int
	ExactLibraryMatches   int
	Rejected              map[string]string
	ModelAvailable        bool
	ModelFallbackReason   string
}

// Analysis is the complete, field-independent result of metadata analysis.
type Analysis struct {
	PerformerCandidates       []Candidate
	UniquePotentialPerformers int
	MatchedPerformerIDs       []int
	StudioCandidate           *Candidate
	StudioID                  *int
	Date                      *ResolvedDate
	Title                     *TitleProposal
	Group                     *GroupProposal
	Technical                 TechnicalMetadata
	Diagnostics               Diagnostics
}

// EntitySpan is an extractor result before it is associated with a Source.
type EntitySpan struct {
	ByteStart int
	ByteEnd   int
	Text      string
	Label     string
	Score     float64
}

// EntityExtractor supplies optional local typed entity evidence. Its label
// prompt is fixed by the extractor implementation and its model tensor shape.
type EntityExtractor interface {
	Extract(ctx context.Context, text string) ([]EntitySpan, error)
}

// Inputs contains bounded typed sources and optional learned extraction.
type Inputs struct {
	Sources                   []Source
	EntityExtractor           EntityExtractor
	AdditionalSpans           []Span
	DateSignals               []DateSignal
	EntityConfidenceThreshold float64
	SanityBound               time.Time
}

// Analyzer runs the source-independent metadata pipeline.
type Analyzer struct{}

// Analyze is implemented by the unified deterministic/entity pipeline.
func (Analyzer) Analyze(ctx context.Context, inputs Inputs) (Analysis, error) {
	return analyze(ctx, inputs)
}
