package urlbuilders

import (
	"strconv"

	"github.com/stashapp/stash/pkg/models"
)

type ClipURLBuilder struct {
	BaseURL string
	ClipID  string
}

func NewClipURLBuilder(baseURL string, clip *models.Clip) ClipURLBuilder {
	return ClipURLBuilder{
		BaseURL: baseURL,
		ClipID:  strconv.Itoa(clip.ID),
	}
}

func (b ClipURLBuilder) GetStreamURL() string {
	return b.BaseURL + "/clip/" + b.ClipID + "/stream"
}

func (b ClipURLBuilder) GetPreviewURL() string {
	return b.BaseURL + "/clip/" + b.ClipID + "/preview"
}
