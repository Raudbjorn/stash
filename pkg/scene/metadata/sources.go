package metadata

import (
	"sort"
	"strings"
	"unicode/utf8"
)

const MaxSourceBytes = 4096

type ContainerMetadata struct {
	Title   string
	Comment string
	Encoder string
	Tags    map[string]string
}

type SourceInputs struct {
	FilenameStem string
	SceneTitle   string
	Details      string
	UseDetails   bool
	NFO          *NFOData
	Container    ContainerMetadata
}

func boundedText(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= MaxSourceBytes {
		return value
	}
	value = value[:MaxSourceBytes]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return strings.TrimSpace(value)
}

func appendSource(sources []Source, kind SourceKind, label, text string, trust TrustTier) []Source {
	text = boundedText(text)
	if text == "" {
		return sources
	}
	return append(sources, NormalizeSource(Source{Kind: kind, Label: label, RawText: text, Trust: trust}))
}

// CollectSources creates deterministic, bounded records without discarding
// duplicate text from different provenance. Learned extraction deduplicates
// calls separately while explicit evidence keeps each source.
func CollectSources(inputs SourceInputs) []Source {
	var sources []Source
	sources = appendSource(sources, SourceFilename, "primary filename", inputs.FilenameStem, TrustFreeText)
	sources = appendSource(sources, SourceSceneTitle, "scene title", inputs.SceneTitle, TrustFreeText)
	if inputs.UseDetails {
		sources = appendSource(sources, SourceDetails, "scene details", inputs.Details, TrustFreeText)
	}
	if nfo := inputs.NFO; nfo != nil {
		sources = appendSource(sources, SourceNFO, "nfo title", nfo.Title, TrustExplicit)
		for _, performer := range nfo.Performers {
			sources = appendSource(sources, SourceNFO, "nfo performer", performer, TrustExplicit)
		}
		sources = appendSource(sources, SourceNFO, "nfo studio", nfo.Studio, TrustExplicit)
		sources = appendSource(sources, SourceNFO, "nfo production date", nfo.Date, TrustExplicit)
		sources = appendSource(sources, SourceNFO, "nfo movie", nfo.Group, TrustExplicit)
	}
	sources = appendSource(sources, SourceContainerTitle, "container title", inputs.Container.Title, TrustContainer)
	sources = appendSource(sources, SourceContainerComment, "container comment", inputs.Container.Comment, TrustContainer)
	keys := make([]string, 0, len(inputs.Container.Tags))
	for key := range inputs.Container.Tags {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		sources = appendSource(sources, SourceContainerTag, "container tag "+key, inputs.Container.Tags[key], TrustContainer)
	}
	if inputs.Container.Encoder != "" {
		sources = appendSource(sources, SourceContainerTag, "container encoder", inputs.Container.Encoder, TrustContainer)
	}
	return sources
}
