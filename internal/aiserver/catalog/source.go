// Package catalog fetches plugin indexes and installs what they advertise.
//
// It runs entirely in Go, in the Stash process. That is what keeps the plugin
// management API answering while the Python host is dead: browsing, planning,
// installing and removing are operations on the network and the filesystem, and
// none of them needs an interpreter.
package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/stashapp/stash/internal/aiserver/store"
)

// IndexSchemaVersion is the plugins_index.json version this build understands.
//
// A mismatch is reported rather than ignored: a future index may describe
// plugins in a way this installer would get wrong.
const IndexSchemaVersion = 1

// indexFile is the document every source must serve at its root.
const indexFile = "plugins_index.json"

// Limits. A catalog is a remote document from a third party; none of these are
// theoretical.
const (
	maxIndexBytes = 4 << 20  // 4 MiB
	maxFileBytes  = 32 << 20 // 32 MiB per plugin file
	maxPluginSize = 96 << 20 // 96 MiB per plugin
	maxFileCount  = 500
	fetchTimeout  = 30 * time.Second
)

// ErrInsecureSource rejects a source that is not served over HTTPS.
//
// Plugin code is executed on the user's machine, so a plaintext catalog is an
// invitation to swap it in transit. Localhost is exempt: a developer serving a
// catalog from their own machine is not exposed to that.
var ErrInsecureSource = errors.New("plugin sources must use https")

// ErrSchemaMismatch reports an index this build cannot read.
var ErrSchemaMismatch = errors.New("unsupported catalog schema version")

// Index is the parsed plugins_index.json.
type Index struct {
	SchemaVersion int          `json:"schemaVersion"`
	Plugins       []IndexEntry `json:"plugins"`
}

// IndexEntry is one advertised plugin.
//
// Both spellings of every field are accepted: the official catalog uses
// camelCase, hand-written ones tend to snake_case, and rejecting either would
// break real catalogs for no benefit.
type IndexEntry struct {
	Name       string `json:"name"`
	PluginName string `json:"plugin_name"`

	Version     string `json:"version"`
	Description string `json:"description"`

	HumanName  string `json:"humanName"`
	HumanName2 string `json:"human_name"`

	ServerLink  string `json:"serverLink"`
	ServerLink2 string `json:"server_link"`

	RequiredBackend  string `json:"requiredBackend"`
	RequiredBackend2 string `json:"required_backend"`

	DependsOn  any `json:"dependsOn"`
	DependsOn2 any `json:"depends_on"`

	// Path is the plugin's subdirectory in the source repository. Absent means
	// the plugin name is the directory, which is what the official catalog does.
	Path  string `json:"path"`
	Path2 string `json:"plugin_path"`

	// Files, when present, lists exactly what to download along with each
	// file's sha256. An index that supplies this gets verified downloads; one
	// that does not falls back to enumerating the repository.
	Files []IndexFile `json:"files"`
}

// IndexFile is one file of a plugin, with its expected digest.
type IndexFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// resolvedName returns the plugin's name under either spelling.
func (e IndexEntry) resolvedName() string {
	if e.Name != "" {
		return e.Name
	}
	return e.PluginName
}

func (e IndexEntry) resolvedPath() string {
	for _, candidate := range []string{e.Path, e.Path2} {
		if candidate != "" {
			return candidate
		}
	}
	return e.resolvedName()
}

func (e IndexEntry) resolvedHumanName() string  { return firstNonEmpty(e.HumanName, e.HumanName2) }
func (e IndexEntry) resolvedServerLink() string { return firstNonEmpty(e.ServerLink, e.ServerLink2) }
func (e IndexEntry) resolvedBackend() string {
	return firstNonEmpty(e.RequiredBackend, e.RequiredBackend2)
}

// resolvedDependencies normalises the several shapes dependsOn appears in.
func (e IndexEntry) resolvedDependencies() []string {
	raw := e.DependsOn
	if raw == nil {
		raw = e.DependsOn2
	}

	var out []string
	appendName := func(v any) {
		text, ok := v.(string)
		if !ok {
			return
		}
		text = strings.TrimSpace(text)
		// Catalogs contain the literal string "null" where a value is absent;
		// treating it as a dependency name would make the plugin uninstallable.
		if text == "" || strings.EqualFold(text, "null") || strings.EqualFold(text, "none") {
			return
		}
		out = append(out, text)
	}

	switch typed := raw.(type) {
	case []any:
		for _, item := range typed {
			appendName(item)
		}
	case string:
		appendName(typed)
	}
	return out
}

// Client fetches catalogs and plugin files.
type Client struct {
	// HTTP is the client used for every request. Nil uses a default with a
	// bounded timeout.
	HTTP *http.Client
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: fetchTimeout}
}

// ValidateSourceURL rejects a source that cannot be trusted to serve code.
func ValidateSourceURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid source URL: %w", err)
	}

	switch parsed.Scheme {
	case "https":
		return nil
	case "http":
		// A developer's own machine is not exposed to interception.
		if isLoopback(parsed.Hostname()) {
			return nil
		}
		return ErrInsecureSource
	default:
		return fmt.Errorf("unsupported source scheme %q", parsed.Scheme)
	}
}

func isLoopback(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1" || host == "[::1]"
}

// FetchIndex retrieves and parses a source's plugin index.
func (c *Client) FetchIndex(ctx context.Context, sourceURL string) (Index, error) {
	if err := ValidateSourceURL(sourceURL); err != nil {
		return Index{}, err
	}

	indexURL := strings.TrimRight(sourceURL, "/") + "/" + indexFile

	body, err := c.get(ctx, indexURL, maxIndexBytes)
	if err != nil {
		return Index{}, err
	}

	var index Index
	if err := json.Unmarshal(body, &index); err != nil {
		return Index{}, fmt.Errorf("catalog index is not valid JSON: %w", err)
	}
	if index.SchemaVersion != IndexSchemaVersion {
		return index, fmt.Errorf("%w: expected %d, got %d",
			ErrSchemaMismatch, IndexSchemaVersion, index.SchemaVersion)
	}
	return index, nil
}

// ToEntries converts an index into catalog rows for storage.
func (index Index) ToEntries(sourceID int64) []store.CatalogEntry {
	out := make([]store.CatalogEntry, 0, len(index.Plugins))

	for _, entry := range index.Plugins {
		name := entry.resolvedName()
		if name == "" {
			// An entry with no name cannot be installed or referred to.
			continue
		}

		version := entry.Version
		if version == "" {
			version = "0.0.0"
		}

		// The manifest is stored whole so the installer can read fields this
		// build does not model yet without another round trip.
		manifest := map[string]any{
			"name":             name,
			"version":          version,
			"description":      entry.Description,
			"human_name":       entry.resolvedHumanName(),
			"server_link":      entry.resolvedServerLink(),
			"required_backend": entry.resolvedBackend(),
			"path":             entry.resolvedPath(),
		}
		if len(entry.Files) > 0 {
			manifest["files_manifest"] = entry.Files
		}
		if deps := entry.resolvedDependencies(); len(deps) > 0 {
			manifest["depends_on"] = deps
		}

		row := store.CatalogEntry{
			PluginName:   name,
			Version:      version,
			Manifest:     manifest,
			Dependencies: entry.resolvedDependencies(),
			SourceID:     sourceID,
		}
		if entry.Description != "" {
			row.Description = &entry.Description
		}
		if human := entry.resolvedHumanName(); human != "" {
			row.HumanName = &human
		}
		if link := entry.resolvedServerLink(); link != "" {
			row.ServerLink = &link
		}
		out = append(out, row)
	}
	return out
}

// get retrieves a URL, refusing a body larger than limit.
func (c *Client) get(ctx context.Context, target string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", target, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch %s: HTTP %d", target, resp.StatusCode)
	}

	// LimitReader with one spare byte, so exceeding the cap is detectable
	// rather than silently truncating into a corrupt file.
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", target, err)
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("%s is larger than the %d byte limit", target, limit)
	}
	return body, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
