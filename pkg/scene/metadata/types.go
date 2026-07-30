package metadata

// SourceKind identifies one independently analyzed metadata string.
type SourceKind string

const (
	SourceTitle                 SourceKind = "title"
	SourceFilename              SourceKind = "filename"
	SourceDetails               SourceKind = "details"
	SourceParentFolder          SourceKind = "parent_folder"
	SourceNFOTitle              SourceKind = "nfo_title"
	SourceNFOActor              SourceKind = "nfo_actor"
	SourceNFOStudio             SourceKind = "nfo_studio"
	SourceNFOPremiered          SourceKind = "nfo_premiered"
	SourceNFOYear               SourceKind = "nfo_year"
	SourceNFOSet                SourceKind = "nfo_set"
	SourceContainerTitle        SourceKind = "container_title"
	SourceContainerComment      SourceKind = "container_comment"
	SourceContainerDescription  SourceKind = "container_description"
	SourceContainerDate         SourceKind = "container_date"
	SourceContainerCreationTime SourceKind = "container_creation_time"
	SourceContainerArtist       SourceKind = "container_artist"
	SourceContainerPublisher    SourceKind = "container_publisher"
	SourceContainerShow         SourceKind = "container_show"
	SourceContainerEpisode      SourceKind = "container_episode"
	SourceExifDescription       SourceKind = "exif_description"
	SourceExifArtist            SourceKind = "exif_artist"
	SourceExifOriginalDate      SourceKind = "exif_original_date"
)

// TextSource preserves the origin of an independently analyzed string.
type TextSource struct {
	Text    string
	Kind    SourceKind
	Path    string
	Ordinal int
}

// SourceCount reports the number of performer occurrences in one source.
type SourceCount struct {
	Kind    SourceKind
	Path    string
	Ordinal int
	Count   int
}

// EntityKind is the normalized analyzer entity vocabulary.
type EntityKind string

const (
	EntityPerformer        EntityKind = "performer"
	EntityProductionStudio EntityKind = "production studio"
	EntityProductionDate   EntityKind = "production date"
	EntitySceneTitle       EntityKind = "scene title"
	EntityMovieTitle       EntityKind = "movie title"
	EntitySceneNumber      EntityKind = "scene number"
	EntityReleaseGroup     EntityKind = "release group"
)

// ExtractorKind records which detector produced an entity proposal.
type ExtractorKind string

const (
	ExtractorStructured ExtractorKind = "structured"
	ExtractorExact      ExtractorKind = "exact"
	ExtractorGLiNER     ExtractorKind = "gliner"
	ExtractorLegacy     ExtractorKind = "legacy"
)

// EntitySpan is a provenance-preserving entity proposal with half-open UTF-8
// byte offsets into Source.Text.
type EntitySpan struct {
	Text       string
	Kind       EntityKind
	Start      int
	End        int
	Confidence float64
	Extractor  ExtractorKind
	Source     TextSource
}
