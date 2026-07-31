package python

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

var indexNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func DefaultIndexes() []Index {
	return []Index{{Name: "pypi", URL: "https://pypi.org/simple/", Default: true}}
}

func ValidateIndexes(indexes []Index) ([]Index, error) {
	if len(indexes) == 0 {
		return nil, errors.New("at least one Python package index is required")
	}
	ret := make([]Index, len(indexes))
	names := make(map[string]struct{}, len(indexes))
	urls := make(map[string]struct{}, len(indexes))
	defaults := 0
	for n, index := range indexes {
		index.Name = strings.TrimSpace(index.Name)
		index.URL = strings.TrimSpace(index.URL)
		if !indexNamePattern.MatchString(index.Name) {
			return nil, fmt.Errorf("invalid Python package index name %q", index.Name)
		}
		nameKey := strings.ToLower(index.Name)
		if _, exists := names[nameKey]; exists {
			return nil, fmt.Errorf("duplicate Python package index name %q", index.Name)
		}
		names[nameKey] = struct{}{}
		canonicalURL, err := canonicalIndexURL(index.URL)
		if err != nil {
			return nil, fmt.Errorf("invalid Python package index %q: %w", index.Name, err)
		}
		if _, exists := urls[canonicalURL]; exists {
			return nil, fmt.Errorf("duplicate Python package index URL %q", canonicalURL)
		}
		urls[canonicalURL] = struct{}{}
		index.URL = canonicalURL
		if index.Default {
			defaults++
		}
		ret[n] = index
	}
	if defaults != 1 {
		return nil, errors.New("exactly one Python package index must be the default")
	}
	return ret, nil
}

func canonicalIndexURL(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if !parsed.IsAbs() || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", errors.New("URL must be an absolute HTTP or HTTPS URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("URL must not contain credentials, a query, or a fragment")
	}
	if parsed.Hostname() == "" {
		return "", errors.New("URL must include a host")
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	host := strings.ToLower(parsed.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if parsed.Port() != "" {
		host = net.JoinHostPort(parsed.Hostname(), parsed.Port())
		host = strings.ToLower(host)
	}
	parsed.Host = host
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/"
	if parsed.Path == "" {
		parsed.Path = "/"
	}
	parsed.RawPath = ""
	return parsed.String(), nil
}

func (m *Manager) WriteCommandConfig(indexes []Index) (string, func(), error) {
	normalized, err := ValidateIndexes(indexes)
	if err != nil {
		return "", nil, err
	}
	if err := m.ensureDirectories(); err != nil {
		return "", nil, err
	}
	payload, err := toml.Marshal(struct {
		Indexes []Index `toml:"index"`
	}{Indexes: normalized})
	if err != nil {
		return "", nil, err
	}
	file, err := os.CreateTemp(m.cacheDir, ".uv-config-*.toml")
	if err != nil {
		return "", nil, err
	}
	path := file.Name()
	cleanup := func() { _ = os.Remove(path) }
	if err := file.Chmod(0600); err != nil {
		file.Close()
		cleanup()
		return "", nil, err
	}
	if _, err := file.Write(payload); err != nil {
		file.Close()
		cleanup()
		return "", nil, err
	}
	if err := file.Close(); err != nil {
		cleanup()
		return "", nil, err
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		cleanup()
		return "", nil, err
	}
	return absolute, cleanup, nil
}
