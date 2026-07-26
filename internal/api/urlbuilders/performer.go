package urlbuilders

import (
	"strconv"

	"github.com/stashapp/stash/pkg/models"
)

type PerformerURLBuilder struct {
	BaseURL     string
	PerformerID string
	UpdatedAt   string
}

func NewPerformerURLBuilder(baseURL string, performer *models.Performer) PerformerURLBuilder {
	return PerformerURLBuilder{
		BaseURL:     baseURL,
		PerformerID: strconv.Itoa(performer.ID),
		UpdatedAt:   strconv.FormatInt(performer.UpdatedAt.Unix(), 10),
	}
}

func (b PerformerURLBuilder) GetPerformerImageURL(hasImage bool) string {
	url := b.BaseURL + "/performer/" + b.PerformerID + "/image?t=" + b.UpdatedAt
	if !hasImage {
		url += "&default=true"
	}
	return url
}

// GetPerformerImageURLByIndex builds the serve URL for the Nth image in the
// ordered collection. Index 0 is byte-identical to GetPerformerImageURL(true)
// (the primary, mirrored into image_path); higher indices add &index=N.
func (b PerformerURLBuilder) GetPerformerImageURLByIndex(index int) string {
	url := b.BaseURL + "/performer/" + b.PerformerID + "/image?t=" + b.UpdatedAt
	if index > 0 {
		url += "&index=" + strconv.Itoa(index)
	}
	return url
}
