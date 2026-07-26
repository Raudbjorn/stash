package image

import (
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/rwcarlsen/goexif/exif"
)

// ErrNoExifData is returned by ExtractExifData when the image has no EXIF
// segment to decode.
var ErrNoExifData = errors.New("no EXIF data")

// exifThumbnailTags are stripped from the decoded output since they carry
// embedded binary thumbnail data rather than useful metadata.
var exifThumbnailTags = map[string]struct{}{
	"ThumbJPEGInterchangeFormat":       {},
	"ThumbJPEGInterchangeFormatLength": {},
}

// ExtractExifData reads and decodes all EXIF fields present in r into a
// generic map, suitable for exposure via the GraphQL Map scalar. Returns
// ErrNoExifData if the file has no EXIF segment.
func ExtractExifData(r io.Reader) (map[string]interface{}, error) {
	x, err := exif.Decode(r)
	if err != nil {
		if errors.Is(err, io.EOF) || strings.Contains(err.Error(), "failed to find exif") {
			return nil, ErrNoExifData
		}
		return nil, err
	}

	b, err := x.MarshalJSON()
	if err != nil {
		return nil, err
	}

	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}

	for tag := range exifThumbnailTags {
		delete(m, tag)
	}

	return m, nil
}
