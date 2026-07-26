package image

import (
	"encoding/json"
	"errors"
	"io"

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
		// The pinned goexif version (v0.0.0-20190401172101-9e8deecbddbd) has
		// no exported sentinel for "no EXIF segment present" - it only
		// returns io.EOF (short/empty file) or a plain errors.New with this
		// exact text (exif/exif.go's readImageStructure). Matched on the
		// full string, not a substring, to fail loudly (fall through to the
		// generic error path below) rather than silently if a future
		// goexif bump changes it - re-check for a real sentinel on upgrade.
		if errors.Is(err, io.EOF) || err.Error() == "exif: failed to find exif intro marker" {
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
