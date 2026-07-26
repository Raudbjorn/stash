// Package imagemagick provides a thin wrapper around the ImageMagick
// convert/magick binary, used as a fallback image probe when ffprobe
// cannot read an image file.
package imagemagick

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	"github.com/stashapp/stash/pkg/fsutil"
)

// IMConvert is the path to the ImageMagick convert/magick binary.
type IMConvert string

// NewImageFile runs ImageMagick on the given path and returns the parsed
// probe entry describing the image (format, dimensions, etc.).
func (f *IMConvert) NewImageFile(imagePath string) (*IMProbeJSONEntry, error) {
	if err := f.Available(); err != nil {
		return nil, err
	}

	args := []string{imagePath, "json:"}

	cmd := exec.Command(string(*f), f.getV7Fix(args)...)
	out, err := cmd.Output()

	if err != nil {
		return nil, fmt.Errorf("ImageMagick convert encountered an error with <%s>.\nError JSON:\n%s\nError: %s", imagePath, string(out), err.Error())
	}

	probeJSON := &IMProbeJSON{}
	if err := json.Unmarshal(out, probeJSON); err != nil || len(*probeJSON) == 0 {
		return nil, fmt.Errorf("error unmarshalling image data for <%s>: %v", imagePath, err)
	}

	elementAtIndex0 := (*probeJSON)[0]
	return &elementAtIndex0, nil
}

// GetPaths returns the path to the ImageMagick binary, checking the system
// PATH for "convert" and "magick" and then the supplied config directories.
// Returns an empty string if ImageMagick could not be located.
func GetPaths(paths []string) string {
	var convertPath string

	// Check if ImageMagick exists in the PATH
	convertPath, _ = exec.LookPath("convert")

	// Check if ImageMagick exists in the config directory
	if convertPath == "" {
		convertPath = fsutil.FindInPaths(paths, getConvertFilename())
	}

	// Check if ImageMagick exists in the PATH
	if convertPath == "" {
		convertPath, _ = exec.LookPath("magick")
	}

	// Check if ImageMagick exists in the config directory
	if convertPath == "" {
		convertPath = fsutil.FindInPaths(paths, getMagickFilename())
	}

	return convertPath
}

// getV7Fix prepends the "convert" subcommand when using the ImageMagick v7
// "magick" entrypoint, which requires it. v6 "convert" is invoked directly.
func (f *IMConvert) getV7Fix(args []string) []string {
	if strings.HasSuffix(string(*f), "magick") {
		return append([]string{"convert"}, args...)
	}

	return args
}

// Available returns an error if the ImageMagick binary path is not set.
func (f *IMConvert) Available() error {
	if string(*f) == "" {
		return fmt.Errorf("ImageMagick not found")
	}

	return nil
}

// getConvertFilename returns the ImageMagick v6 binary filename for the current OS.
func getConvertFilename() string {
	if runtime.GOOS == "windows" {
		return "convert.exe"
	}
	return "convert"
}

// getMagickFilename returns the ImageMagick v7+ binary filename for the current OS.
func getMagickFilename() string {
	if runtime.GOOS == "windows" {
		return "magick.exe"
	}
	return "magick"
}
