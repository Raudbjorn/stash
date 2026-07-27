package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/stashapp/stash/internal/aiserver/store"
)

// Downloading plugin code.
//
// Two paths, and the difference matters. An index that lists its files with
// sha256 digests gets verified downloads - what arrives is exactly what the
// catalog author published, and a compromised CDN cannot substitute anything.
// An index without them falls back to enumerating the repository, which is what
// the existing official catalog requires; that path is honest about being
// unverified rather than pretending otherwise.

// ErrUnsafePath rejects an archive path that would escape its destination.
var ErrUnsafePath = errors.New("refusing a plugin file path outside its directory")

// ErrDigestMismatch reports a file that is not what the catalog promised.
var ErrDigestMismatch = errors.New("downloaded file does not match its published checksum")

// ErrUnverifiedManifest rejects a declared file list with missing digests.
var ErrUnverifiedManifest = errors.New("catalog file manifest must checksum every file")

// DownloadedFile is one file fetched for a plugin.
type DownloadedFile struct {
	// RelPath is the path within the plugin directory, always slash-separated
	// and always relative.
	RelPath string
	Data    []byte
	// Verified reports whether a published digest was checked.
	Verified bool
}

// Download fetches a plugin's files from its source.
func (c *Client) Download(ctx context.Context, sourceURL string, entry store.CatalogEntry) ([]DownloadedFile, error) {
	if err := ValidateSourceURL(sourceURL); err != nil {
		return nil, err
	}

	pluginPath, files, declared, err := readManifestLocation(entry)
	if err != nil {
		return nil, err
	}
	pluginPath, err = safeRelPath(pluginPath)
	if err != nil {
		return nil, fmt.Errorf("plugin manifest path: %w", err)
	}
	if declared {
		return c.downloadListed(ctx, sourceURL, pluginPath, files)
	}
	return c.downloadTree(ctx, sourceURL, pluginPath)
}

// readManifestLocation extracts where a plugin lives and, when published, what
// files it consists of.
//
// The manifest is stored as opaque JSON so a newer catalog can carry fields
// this build does not model; reading it back is therefore defensive rather than
// typed.
func readManifestLocation(entry store.CatalogEntry) (string, []IndexFile, bool, error) {
	pluginPath := entry.PluginName
	if raw, ok := entry.Manifest["path"]; ok {
		pathValue, valid := raw.(string)
		if !valid || pathValue == "" {
			return "", nil, false, fmt.Errorf("catalog path for %s is not a non-empty string", entry.PluginName)
		}
		pluginPath = pathValue
	}

	rawFiles, declared := entry.Manifest["files_manifest"]
	if !declared {
		return pluginPath, nil, false, nil
	}
	encoded, err := json.Marshal(rawFiles)
	if err != nil {
		return "", nil, true, fmt.Errorf("encode file manifest for %s: %w", entry.PluginName, err)
	}
	var files []IndexFile
	if err := json.Unmarshal(encoded, &files); err != nil {
		return "", nil, true, fmt.Errorf("decode file manifest for %s: %w", entry.PluginName, err)
	}
	if len(files) == 0 {
		return "", nil, true, fmt.Errorf("%w: %s declares no files", ErrUnverifiedManifest, entry.PluginName)
	}
	return pluginPath, files, true, nil
}

// downloadListed fetches exactly the files the index named, verifying each.
//
// This is the path worth having: the set of files is fixed in advance, so a
// source cannot add one, and every byte is checked against a digest published
// alongside the rest of the index.
func (c *Client) downloadListed(ctx context.Context, sourceURL, pluginPath string, files []IndexFile) ([]DownloadedFile, error) {
	if len(files) > maxFileCount {
		return nil, fmt.Errorf("plugin declares %d files, more than the %d limit", len(files), maxFileCount)
	}
	for _, file := range files {
		decoded, err := hex.DecodeString(file.SHA256)
		if err != nil || len(decoded) != sha256.Size {
			return nil, fmt.Errorf("%w: %s", ErrUnverifiedManifest, file.Path)
		}
	}

	base := strings.TrimRight(sourceURL, "/")
	var (
		out   []DownloadedFile
		total int64
	)

	for _, file := range files {
		rel, err := safeRelPath(file.Path)
		if err != nil {
			return nil, err
		}

		limit := int64(maxFileBytes)
		if file.Size > 0 && file.Size < limit {
			// Trust the declared size as a tighter cap, but never a looser one.
			limit = file.Size
		}

		target := base + "/" + path.Join(pluginPath, rel)
		data, err := c.get(ctx, target, limit)
		if err != nil {
			return nil, err
		}

		sum := sha256.Sum256(data)
		if !strings.EqualFold(hex.EncodeToString(sum[:]), file.SHA256) {
			return nil, fmt.Errorf("%w: %s", ErrDigestMismatch, rel)
		}
		if file.Size > 0 && int64(len(data)) != file.Size {
			return nil, fmt.Errorf("catalog file %s: expected %d bytes, got %d",
				rel, file.Size, len(data))
		}

		total += int64(len(data))
		if total > maxPluginSize {
			return nil, fmt.Errorf("plugin exceeds the %d byte size limit", maxPluginSize)
		}

		out = append(out, DownloadedFile{
			RelPath:  rel,
			Data:     data,
			Verified: true,
		})
	}
	return out, nil
}

// ghContent is one entry of a GitHub contents API listing.
type ghContent struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	Type        string `json:"type"`
	Size        int64  `json:"size"`
	DownloadURL string `json:"download_url"`
}

// downloadTree enumerates a repository subdirectory and fetches it.
//
// Kept because the existing official catalog has no file manifest and would
// otherwise stop working. Everything it produces is marked unverified, so the
// distinction reaches the caller rather than being lost.
func (c *Client) downloadTree(ctx context.Context, sourceURL, pluginPath string) ([]DownloadedFile, error) {
	owner, repo, branch, err := parseGitHubBase(sourceURL)
	if err != nil {
		return nil, err
	}

	var (
		out   []DownloadedFile
		total int64
	)

	var walk func(repoPath, relBase string, depth int) error
	walk = func(repoPath, relBase string, depth int) error {
		// A malicious or looping listing must not recurse forever.
		if depth > 8 {
			return fmt.Errorf("plugin directory nests deeper than 8 levels")
		}

		apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/contents/%s?ref=%s",
			url.PathEscape(owner), url.PathEscape(repo),
			escapePath(repoPath), url.QueryEscape(branch))

		body, err := c.get(ctx, apiURL, maxIndexBytes)
		if err != nil {
			return err
		}

		// A single file responds with an object, a directory with an array.
		var single ghContent
		if err := json.Unmarshal(body, &single); err == nil && single.Type == "file" {
			return c.fetchInto(ctx, single, relBase, &out, &total)
		}

		var listing []ghContent
		if err := json.Unmarshal(body, &listing); err != nil {
			return fmt.Errorf("unexpected repository listing for %s: %w", repoPath, err)
		}

		for _, item := range listing {
			if len(out) >= maxFileCount {
				return fmt.Errorf("plugin contains more than %d files", maxFileCount)
			}
			switch item.Type {
			case "file":
				if err := c.fetchInto(ctx, item, relBase, &out, &total); err != nil {
					return err
				}
			case "dir":
				if err := walk(item.Path, path.Join(relBase, item.Name), depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	}

	if err := walk(strings.Trim(pluginPath, "/"), "", 0); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no files found at %s", pluginPath)
	}
	return out, nil
}

func (c *Client) fetchInto(ctx context.Context, item ghContent, relBase string, out *[]DownloadedFile, total *int64) error {
	if item.DownloadURL == "" {
		return nil
	}

	rel, err := safeRelPath(path.Join(relBase, item.Name))
	if err != nil {
		return err
	}

	data, err := c.get(ctx, item.DownloadURL, maxFileBytes)
	if err != nil {
		return err
	}

	*total += int64(len(data))
	if *total > maxPluginSize {
		return fmt.Errorf("plugin exceeds the %d byte size limit", maxPluginSize)
	}

	*out = append(*out, DownloadedFile{RelPath: rel, Data: data})
	return nil
}

// parseGitHubBase extracts owner, repo and branch from a source URL.
func parseGitHubBase(sourceURL string) (owner, repo, branch string, err error) {
	parsed, parseErr := url.Parse(sourceURL)
	if parseErr != nil {
		return "", "", "", parseErr
	}

	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	switch parsed.Host {
	case "raw.githubusercontent.com":
		// /<owner>/<repo>/<branch>[/...]
		if len(parts) < 3 {
			return "", "", "", fmt.Errorf("cannot read owner, repo and branch from %s", sourceURL)
		}
		return parts[0], parts[1], parts[2], nil
	case "github.com", "www.github.com":
		if len(parts) < 2 {
			return "", "", "", fmt.Errorf("cannot read owner and repo from %s", sourceURL)
		}
		return parts[0], parts[1], "main", nil
	default:
		return "", "", "", fmt.Errorf(
			"source %s has no file manifest, and only GitHub repositories can be enumerated without one", sourceURL)
	}
}

func escapePath(p string) string {
	segments := strings.Split(strings.Trim(p, "/"), "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}
	return strings.Join(segments, "/")
}

// safeRelPath rejects anything that would write outside the plugin directory.
//
// The threat is concrete: a catalog entry naming "../../../.ssh/authorized_keys"
// would otherwise be written wherever the path led.
//
// Traversal is REJECTED, not normalised away. path.Clean would happily turn
// "../evil.py" into "evil.py", which is safe but wrong: the file would be
// written somewhere the catalog did not name, and an author who published a
// bad path would never find out.
func safeRelPath(raw string) (string, error) {
	unsafe := func() (string, error) {
		return "", fmt.Errorf("%w: %q", ErrUnsafePath, raw)
	}

	normalised := strings.ReplaceAll(raw, "\\", "/")
	if normalised == "" {
		return unsafe()
	}
	// Absolute, or drive-qualified on Windows.
	if strings.HasPrefix(normalised, "/") || filepath.IsAbs(raw) ||
		(runtime.GOOS == "windows" && strings.Contains(raw, ":")) {
		return unsafe()
	}

	for _, segment := range strings.Split(normalised, "/") {
		if segment == ".." {
			return unsafe()
		}
	}

	cleaned := path.Clean(normalised)
	if cleaned == "" || cleaned == "." || strings.HasPrefix(cleaned, "../") {
		return unsafe()
	}
	return cleaned, nil
}

// WriteFiles writes downloaded files beneath dir.
//
// Every path is re-checked here rather than trusting the download stage: this
// is the last point before bytes hit the filesystem, and a check at the
// boundary that matters is worth repeating.
func WriteFiles(dir string, files []DownloadedFile) error {
	root, err := filepath.Abs(dir)
	if err != nil {
		return err
	}

	for _, file := range files {
		rel, err := safeRelPath(file.RelPath)
		if err != nil {
			return err
		}

		target := filepath.Join(root, filepath.FromSlash(rel))
		if !strings.HasPrefix(target, root+string(os.PathSeparator)) {
			return fmt.Errorf("%w: %q", ErrUnsafePath, file.RelPath)
		}

		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, file.Data, 0o644); err != nil {
			return err
		}
	}
	return nil
}
