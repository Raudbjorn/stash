package python

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"golang.org/x/net/html"
)

const (
	maxCatalogBodySize = 128 << 20
	maxCatalogProjects = 1_500_000
	catalogStaleAfter  = 24 * time.Hour
)

func (m *Manager) catalogDatabase() (*sql.DB, error) {
	m.catalogMu.Lock()
	defer m.catalogMu.Unlock()
	if m.catalogDB != nil {
		return m.catalogDB, nil
	}
	if err := m.ensureDirectories(); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite3", filepath.Join(m.catalogsDir, "catalog.sqlite")+"?_busy_timeout=5000&_foreign_keys=on")
	if err != nil {
		return nil, err
	}
	statements := []string{
		`CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`,
		`INSERT INTO schema_version(version) SELECT 1 WHERE NOT EXISTS (SELECT 1 FROM schema_version)`,
		`CREATE TABLE IF NOT EXISTS indexes (
			name TEXT PRIMARY KEY COLLATE NOCASE, url TEXT NOT NULL, etag TEXT, last_modified TEXT,
			refreshed_at TEXT, error_at TEXT, error TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS projects (
			index_name TEXT NOT NULL COLLATE NOCASE, normalized_name TEXT NOT NULL, name TEXT NOT NULL,
			PRIMARY KEY(index_name, normalized_name)
		)`,
		`CREATE INDEX IF NOT EXISTS projects_normalized_name ON projects(normalized_name)`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			db.Close()
			return nil, err
		}
	}
	m.catalogDB = db
	return db, nil
}

func (m *Manager) RefreshCatalog(ctx context.Context, index Index, reporter Reporter) error {
	m.catalogRefreshMu.Lock()
	defer m.catalogRefreshMu.Unlock()

	normalized, err := ValidateIndexes([]Index{{Name: index.Name, URL: index.URL, Default: true}})
	if err != nil {
		return err
	}
	index = normalized[0]
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	db, err := m.catalogDatabase()
	if err != nil {
		return err
	}
	var storedURL string
	var etag, lastModified sql.NullString
	scanErr := db.QueryRowContext(
		ctx,
		`SELECT url, etag, last_modified FROM indexes WHERE name = ?`,
		index.Name,
	).Scan(&storedURL, &etag, &lastModified)
	if scanErr != nil && !errors.Is(scanErr, sql.ErrNoRows) {
		return scanErr
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, index.URL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.pypi.simple.v1+json")
	reusedValidator := scanErr == nil && storedURL == index.URL && (etag.Valid || lastModified.Valid)
	if scanErr == nil && storedURL == index.URL {
		if etag.Valid {
			req.Header.Set("If-None-Match", etag.String)
		}
		if lastModified.Valid {
			req.Header.Set("If-Modified-Since", lastModified.String)
		}
	}
	client := *m.httpClient
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		if len(via) > 0 && via[len(via)-1].URL.Scheme == "https" && req.URL.Scheme != "https" {
			return errors.New("catalog redirect cannot downgrade HTTPS")
		}
		return nil
	}
	if reporter != nil {
		reporter(Event{Phase: PhaseResolving, Message: "Refreshing " + index.Name})
	}
	response, err := client.Do(req)
	if err != nil {
		m.recordCatalogError(db, index, err)
		return err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotModified {
		if !reusedValidator {
			err = fmt.Errorf("package index %s returned unexpected %s", index.Name, response.Status)
			m.recordCatalogError(db, index, err)
			return err
		}
		now := m.now().UTC().Format(time.RFC3339Nano)
		_, err = db.ExecContext(ctx, `UPDATE indexes SET refreshed_at=?, error_at=NULL, error=NULL WHERE name=?`, now, index.Name)
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		err = fmt.Errorf("package index %s returned %s", index.Name, response.Status)
		m.recordCatalogError(db, index, err)
		return err
	}
	if response.ContentLength > maxCatalogBodySize {
		err = errors.New("package catalog exceeds 128 MiB")
		m.recordCatalogError(db, index, err)
		return err
	}

	body, err := os.CreateTemp(m.catalogsDir, ".catalog-body-*")
	if err != nil {
		return err
	}
	bodyPath := body.Name()
	defer os.Remove(bodyPath)
	if err := body.Chmod(0600); err != nil {
		body.Close()
		return err
	}
	written, copyErr := io.Copy(body, io.LimitReader(response.Body, maxCatalogBodySize+1))
	closeErr := body.Close()
	switch {
	case copyErr != nil:
		err = copyErr
	case closeErr != nil:
		err = closeErr
	case written > maxCatalogBodySize:
		err = errors.New("package catalog exceeds 128 MiB")
	}
	if err != nil {
		m.recordCatalogError(db, index, err)
		return err
	}

	mediaType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	projects, err := parseCatalogFile(bodyPath, mediaType)
	if err != nil {
		m.recordCatalogError(db, index, err)
		return err
	}
	if len(projects) > maxCatalogProjects {
		err = errors.New("package catalog exceeds 1.5 million projects")
		m.recordCatalogError(db, index, err)
		return err
	}
	if reporter != nil {
		reporter(Event{Phase: PhaseInstalling, Message: "Indexing " + index.Name})
	}
	if err := m.replaceCatalogProjects(ctx, db, index, response, projects); err != nil {
		m.recordCatalogError(db, index, err)
		return err
	}
	if reporter != nil {
		reporter(Event{Phase: PhaseComplete, Message: fmt.Sprintf("Indexed %d projects", len(projects))})
	}
	return nil
}

func parseCatalogFile(path, mediaType string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if mediaType == "application/vnd.pypi.simple.v1+json" || mediaType == "application/json" {
		return parsePEP691(file)
	}
	return parsePEP503(file)
}

func parsePEP691(reader io.Reader) (map[string]string, error) {
	decoder := json.NewDecoder(reader)
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errors.New("unsupported PEP 691 representation")
	}
	projects := make(map[string]string)
	found := false
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, _ := keyToken.(string)
		if key != "projects" {
			var discard json.RawMessage
			if err := decoder.Decode(&discard); err != nil {
				return nil, err
			}
			continue
		}
		found = true
		if token, err := decoder.Token(); err != nil || token != json.Delim('[') {
			return nil, errors.New("unsupported PEP 691 projects representation")
		}
		for decoder.More() {
			var project struct {
				Name string `json:"name"`
			}
			if err := decoder.Decode(&project); err != nil {
				return nil, err
			}
			if project.Name == "" {
				continue
			}
			projects[normalizeProjectName(project.Name)] = project.Name
			if len(projects) > maxCatalogProjects {
				return nil, errors.New("package catalog exceeds 1.5 million projects")
			}
		}
		if _, err := decoder.Token(); err != nil {
			return nil, err
		}
	}
	if !found {
		return nil, errors.New("unsupported PEP 691 representation")
	}
	return projects, nil
}

func parsePEP503(reader io.Reader) (map[string]string, error) {
	tokenizer := html.NewTokenizer(reader)
	projects := make(map[string]string)
	inAnchor := false
	anchorHref := ""
	var anchorText strings.Builder
	for {
		tokenType := tokenizer.Next()
		switch tokenType {
		case html.ErrorToken:
			if errors.Is(tokenizer.Err(), io.EOF) {
				return projects, nil
			}
			return nil, tokenizer.Err()
		case html.StartTagToken:
			token := tokenizer.Token()
			if token.Data == "a" {
				inAnchor = true
				anchorText.Reset()
				anchorHref = ""
				for _, attr := range token.Attr {
					if attr.Key == "href" {
						anchorHref = attr.Val
					}
				}
			}
		case html.TextToken:
			if inAnchor {
				anchorText.Write(tokenizer.Text())
			}
		case html.EndTagToken:
			if tokenizer.Token().Data == "a" && inAnchor {
				name := strings.TrimSpace(anchorText.String())
				if name == "" {
					parsed, _ := url.Parse(anchorHref)
					name, _ = url.PathUnescape(strings.Trim(filepath.Base(parsed.Path), "/"))
				}
				if name != "" {
					projects[normalizeProjectName(name)] = name
					if len(projects) > maxCatalogProjects {
						return nil, errors.New("package catalog exceeds 1.5 million projects")
					}
				}
				inAnchor = false
			}
		}
	}
}

func (m *Manager) replaceCatalogProjects(ctx context.Context, db *sql.DB, index Index, response *http.Response, projects map[string]string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `CREATE TEMP TABLE IF NOT EXISTS incoming_projects(normalized_name TEXT PRIMARY KEY, name TEXT NOT NULL)`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM incoming_projects`); err != nil {
		return err
	}
	if err := func() error {
		statement, err := tx.PrepareContext(ctx, `INSERT OR REPLACE INTO incoming_projects(normalized_name,name) VALUES(?,?)`)
		if err != nil {
			return err
		}
		defer statement.Close()
		for normalized, name := range projects {
			if _, err := statement.ExecContext(ctx, normalized, name); err != nil {
				return err
			}
		}
		return nil
	}(); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM projects WHERE index_name=?`, index.Name); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO projects(index_name, normalized_name, name) SELECT ?, normalized_name, name FROM incoming_projects`, index.Name); err != nil {
		return err
	}
	now := m.now().UTC().Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, `INSERT INTO indexes(name,url,etag,last_modified,refreshed_at,error_at,error)
		VALUES(?,?,?,?,?,NULL,NULL) ON CONFLICT(name) DO UPDATE SET url=excluded.url,etag=excluded.etag,
		last_modified=excluded.last_modified,refreshed_at=excluded.refreshed_at,error_at=NULL,error=NULL`,
		index.Name, index.URL, response.Header.Get("ETag"), response.Header.Get("Last-Modified"), now)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (m *Manager) recordCatalogError(db *sql.DB, index Index, catalogErr error) {
	now := m.now().UTC().Format(time.RFC3339Nano)
	_, _ = db.Exec(`INSERT INTO indexes(name,url,error_at,error) VALUES(?,?,?,?)
		ON CONFLICT(name) DO UPDATE SET url=excluded.url,
		etag=CASE WHEN indexes.url=excluded.url THEN indexes.etag ELSE NULL END,
		last_modified=CASE WHEN indexes.url=excluded.url THEN indexes.last_modified ELSE NULL END,
		error_at=excluded.error_at,error=excluded.error`, index.Name, index.URL, now, catalogErr.Error())
}

func (m *Manager) CatalogStatuses(ctx context.Context, indexes []Index) ([]CatalogStatus, error) {
	normalized, err := ValidateIndexes(indexes)
	if err != nil {
		return nil, err
	}
	db, err := m.catalogDatabase()
	if err != nil {
		return nil, err
	}
	statuses := make([]CatalogStatus, 0, len(normalized))
	for _, index := range normalized {
		var refreshed, catalogError sql.NullString
		var count int
		err := db.QueryRowContext(ctx, `SELECT i.refreshed_at, i.error,
			(SELECT COUNT(*) FROM projects p WHERE p.index_name=i.name) FROM indexes i WHERE i.name=?`, index.Name).Scan(&refreshed, &catalogError, &count)
		status := CatalogStatus{Index: index.Name, Stale: true}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if refreshed.Valid {
			when, parseErr := time.Parse(time.RFC3339Nano, refreshed.String)
			if parseErr == nil {
				status.RefreshedAt = &when
				status.Stale = m.now().Sub(when) >= catalogStaleAfter
			}
		}
		status.ProjectCount = count
		if catalogError.Valid {
			status.Error = stringPtr(catalogError.String)
		}
		statuses = append(statuses, status)
	}
	return statuses, nil
}

func (m *Manager) SearchPackages(ctx context.Context, query string, indexName *string, limit int, indexes []Index) ([]PackageSearchResult, error) {
	m.catalogRefreshMu.Lock()
	defer m.catalogRefreshMu.Unlock()

	query = strings.TrimSpace(query)
	if len([]rune(query)) < 2 {
		return nil, errors.New("python package search requires at least two characters")
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	normalizedIndexes, err := ValidateIndexes(indexes)
	if err != nil {
		return nil, err
	}
	known := make(map[string]string, len(normalizedIndexes))
	for _, index := range normalizedIndexes {
		known[strings.ToLower(index.Name)] = index.Name
	}
	selected := ""
	if indexName != nil {
		selected = known[strings.ToLower(*indexName)]
		if selected == "" {
			return nil, fmt.Errorf("unknown Python package index %q", *indexName)
		}
	}
	db, err := m.catalogDatabase()
	if err != nil {
		return nil, err
	}
	normalizedQuery := normalizeProjectName(query)
	like := "%" + escapeLike(normalizedQuery) + "%"
	prefix := escapeLike(normalizedQuery) + "%"
	args := []interface{}{like}
	filter := ""
	if selected != "" {
		filter = " AND index_name = ?"
		args = append(args, selected)
	}
	args = append(args, prefix, limit)
	rows, err := db.QueryContext(ctx, `SELECT normalized_name, MIN(name), GROUP_CONCAT(index_name, char(31))
		FROM projects WHERE normalized_name LIKE ? ESCAPE '\'`+filter+`
		GROUP BY normalized_name ORDER BY CASE WHEN normalized_name LIKE ? ESCAPE '\' THEN 0 ELSE 1 END,
		LENGTH(normalized_name), normalized_name LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var results []PackageSearchResult
	for rows.Next() {
		var normalized, name, joined string
		if err := rows.Scan(&normalized, &name, &joined); err != nil {
			return nil, err
		}
		indexList := strings.Split(joined, string(rune(31)))
		sort.Strings(indexList)
		results = append(results, PackageSearchResult{Name: name, Indexes: indexList})
	}
	return results, rows.Err()
}

func escapeLike(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `%`, `\%`)
	return strings.ReplaceAll(value, `_`, `\_`)
}
