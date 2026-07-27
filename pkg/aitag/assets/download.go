// Package assets downloads and verifies the model files and the ONNX runtime.
//
// Nothing ships in-tree: every model is fetched on explicit opt-in, with its
// licence shown first. What is downloaded is executed - the runtime as native
// code, the models as graphs - so every artefact is checksum-verified, which is
// a check Stash's existing ffmpeg downloader notably lacks and this must not.
package assets

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// ErrChecksumMismatch reports an artefact that is not what was published.
var ErrChecksumMismatch = errors.New("downloaded file does not match its published checksum")

// ErrSizeMismatch reports a response outside its catalogued byte count.
var ErrSizeMismatch = errors.New("downloaded file size does not match its published size")

// ErrInsecureURL rejects a plaintext download.
var ErrInsecureURL = errors.New("model downloads must use https")

// ErrUnsafePath rejects an archive entry that would escape its destination.
var ErrUnsafePath = errors.New("refusing an archive path outside the destination")

// ErrUnsafeRedirect rejects a redirect to a local or otherwise non-public host.
var ErrUnsafeRedirect = errors.New("download redirect must target a public host")

// Licence identifiers, shown before a download is offered.
//
// Carried with the asset rather than in documentation because the user is being
// asked to accept it: a licence in a README nobody reads is not consent.
const (
	LicenseApache2 = "Apache-2.0"
	LicenseMIT     = "MIT"
	LicenseAGPL3   = "AGPL-3.0"
	LicenseBSD3    = "BSD-3-Clause"
)

// Asset is one downloadable file.
type Asset struct {
	// Name identifies it in the catalog and on disk.
	Name string
	// URL is where it is fetched from. HTTPS only.
	URL string
	// SHA256 is the expected digest of the downloaded bytes, before any
	// extraction. Required: an unverified model is arbitrary code from the
	// internet.
	SHA256 string
	// Size is the expected byte count, used for progress and as a cheap guard
	// against a wildly wrong response.
	Size int64

	// Archive names the container format, if any: "zip", "tgz", or "" for a
	// plain file.
	Archive string
	// ExtractPath is the file to take out of the archive, if only one is
	// wanted. Empty extracts everything.
	ExtractPath string

	// License and LicenseURL are shown to the user before downloading.
	License    string
	LicenseURL string
	// Description explains what the asset is for.
	Description string
}

// Progress reports download progress.
type Progress func(downloaded, total int64)

// Downloader fetches assets into a cache directory.
type Downloader struct {
	// Dir is where assets are stored.
	Dir string
	// HTTP overrides the client, for tests.
	HTTP *http.Client
}

const integrityFile = ".asset-integrity.json"

type archiveIntegrity struct {
	ArchiveSHA256 string `json:"archive_sha256"`
	TreeSHA256    string `json:"tree_sha256"`
}

func (d *Downloader) client() *http.Client {
	var client http.Client
	if d.HTTP != nil {
		client = *d.HTTP
	}
	previous := client.CheckRedirect
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if err := validateURL(req.URL.String()); err != nil {
			return err
		}
		if err := validatePublicRedirect(req); err != nil {
			return err
		}
		if previous != nil {
			return previous(req, via)
		}
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return nil
	}
	// No overall timeout: a model is hundreds of megabytes and a slow link is
	// not a failure. The context is the deadline.
	return &client
}

// Path returns where an asset lives once installed.
func (d *Downloader) Path(asset Asset) string {
	if asset.Archive != "" && asset.ExtractPath != "" {
		return filepath.Join(d.Dir, asset.Name, filepath.FromSlash(asset.ExtractPath))
	}
	if asset.Archive != "" {
		return filepath.Join(d.Dir, asset.Name)
	}
	return filepath.Join(d.Dir, asset.Name)
}

// Installed reports whether an asset is already present and verified.
func (d *Downloader) Installed(asset Asset) bool {
	installedPath := d.Path(asset)
	info, err := os.Stat(installedPath)
	if err != nil {
		return false
	}

	if asset.Archive != "" {
		root := filepath.Join(d.Dir, asset.Name)
		data, err := os.ReadFile(filepath.Join(root, integrityFile))
		if err != nil {
			return false
		}
		var integrity archiveIntegrity
		if json.Unmarshal(data, &integrity) != nil ||
			!strings.EqualFold(integrity.ArchiveSHA256, asset.SHA256) {
			return false
		}
		sum, err := treeDigest(root)
		return err == nil && strings.EqualFold(sum, integrity.TreeSHA256)
	}
	if asset.Size > 0 && info.Size() != asset.Size {
		return false
	}
	if asset.SHA256 == "" {
		return true
	}

	sum, err := fileDigest(installedPath)
	if err != nil {
		return false
	}
	return strings.EqualFold(sum, asset.SHA256)
}

// Fetch downloads and installs an asset, returning its path.
//
// A no-op when already installed, so it is safe to call on every startup.
func (d *Downloader) Fetch(ctx context.Context, asset Asset, progress Progress) (string, error) {
	if asset.SHA256 == "" {
		// Refused before Installed is consulted: a pre-existing path must not
		// turn an unpinned native executable or model into a trusted asset.
		return "", fmt.Errorf("asset %s has no published checksum and will not be downloaded", asset.Name)
	}
	if d.Installed(asset) {
		return d.Path(asset), nil
	}

	if err := validateURL(asset.URL); err != nil {
		return "", err
	}

	if err := os.MkdirAll(d.Dir, 0o755); err != nil {
		return "", err
	}

	// Downloaded to a temporary file and only moved into place once verified,
	// so an interrupted download never leaves something that looks installed.
	temp, err := os.CreateTemp(d.Dir, ".download-*")
	if err != nil {
		return "", err
	}
	tempPath := temp.Name()
	defer func() {
		temp.Close()
		os.Remove(tempPath)
	}()

	sum, err := d.download(ctx, asset, temp, progress)
	if err != nil {
		return "", err
	}
	if !strings.EqualFold(sum, asset.SHA256) {
		return "", fmt.Errorf("%w: %s\n  expected %s\n  got      %s",
			ErrChecksumMismatch, asset.Name, asset.SHA256, sum)
	}
	if err := temp.Close(); err != nil {
		return "", err
	}

	target := d.Path(asset)
	if asset.Archive == "" {
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return "", err
		}
		if err := os.Rename(tempPath, target); err != nil {
			return "", err
		}
		// Deliberately NOT executable. POSIX executables must arrive in a
		// mode-bearing archive entry; Windows .exe execution does not depend on
		// POSIX mode bits. Archives also provide a directory for shared
		// libraries.
		_ = os.Chmod(target, 0o644)
		return target, nil
	}

	dest := filepath.Join(d.Dir, asset.Name)
	stage, err := os.MkdirTemp(d.Dir, "."+asset.Name+"-extract-*")
	if err != nil {
		return "", err
	}
	keepStage := false
	defer func() {
		if !keepStage {
			_ = os.RemoveAll(stage)
		}
	}()

	if err := extract(tempPath, stage, asset.Archive); err != nil {
		return "", err
	}
	stagedTarget := stage
	if asset.ExtractPath != "" {
		stagedTarget, err = safeJoin(stage, asset.ExtractPath)
		if err != nil {
			return "", err
		}
	}
	if _, err := os.Stat(stagedTarget); err != nil {
		return "", fmt.Errorf("archive %s does not contain %s", asset.Name, asset.ExtractPath)
	}

	treeSHA256, err := treeDigest(stage)
	if err != nil {
		return "", fmt.Errorf("verify extracted %s: %w", asset.Name, err)
	}
	integrity, err := json.Marshal(archiveIntegrity{
		ArchiveSHA256: strings.ToLower(asset.SHA256),
		TreeSHA256:    treeSHA256,
	})
	if err != nil {
		return "", err
	}
	if err := os.Remove(filepath.Join(stage, integrityFile)); err != nil && !os.IsNotExist(err) {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(stage, integrityFile), integrity, 0o600); err != nil {
		return "", err
	}

	// Directory replacement is deliberately staged, not claimed to be
	// portable-atomic: all extraction and validation completes before the old
	// destination is removed.
	if err := os.RemoveAll(dest); err != nil {
		return "", err
	}
	if err := os.Rename(stage, dest); err != nil {
		return "", err
	}
	keepStage = true
	return d.Path(asset), nil
}

// download streams the asset to w, hashing as it goes.
//
// Hashed while streaming rather than by re-reading: the file is hundreds of
// megabytes, and a second pass doubles the I/O for nothing.
func (d *Downloader) download(ctx context.Context, asset Asset, w io.Writer, progress Progress) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.URL, nil)
	if err != nil {
		return "", err
	}

	resp, err := d.client().Do(req)
	if err != nil {
		return "", fmt.Errorf("download %s: %w", asset.Name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download %s: HTTP %d", asset.Name, resp.StatusCode)
	}

	total := resp.ContentLength
	if asset.Size > 0 {
		total = asset.Size
	}

	hasher := sha256.New()
	counter := &countingWriter{progress: progress, total: total}

	// LimitReader bounds disk use even when a server ignores Content-Length.
	// One extra byte distinguishes an exact response from an oversized one.
	body := io.Reader(&contextReader{ctx: ctx, reader: resp.Body})
	if asset.Size > 0 {
		body = io.LimitReader(body, asset.Size+1)
	}
	n, err := io.Copy(io.MultiWriter(w, hasher, counter), body)
	if err != nil {
		return "", fmt.Errorf("download %s: %w", asset.Name, err)
	}
	if asset.Size > 0 && n != asset.Size {
		return "", fmt.Errorf("%w: %s: expected %d bytes, got %d",
			ErrSizeMismatch, asset.Name, asset.Size, n)
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// contextReader aborts a read when the context ends.
type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

// countingWriter reports progress, rate-limited so a fast download does not
// flood the caller with updates.
type countingWriter struct {
	progress Progress
	total    int64
	written  atomic.Int64
	lastAt   time.Time
}

func (c *countingWriter) Write(p []byte) (int, error) {
	written := c.written.Add(int64(len(p)))
	if c.progress == nil {
		return len(p), nil
	}
	if now := time.Now(); now.Sub(c.lastAt) >= 250*time.Millisecond {
		c.lastAt = now
		c.progress(written, c.total)
	}
	return len(p), nil
}

func validateURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid asset URL %q: %w", raw, err)
	}
	if parsed.Scheme != "https" {
		return fmt.Errorf("%w: %s", ErrInsecureURL, raw)
	}
	return nil
}

func fileDigest(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// ---------------------------------------------------------------- archives --

func extract(archivePath, dest, format string) error {
	switch format {
	case "zip":
		return extractZip(archivePath, dest)
	case "tgz", "tar.gz":
		return extractTarGz(archivePath, dest)
	default:
		return fmt.Errorf("unsupported archive format %q", format)
	}
}

func extractZip(archivePath, dest string) error {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer reader.Close()

	for _, entry := range reader.File {
		target, err := safeJoin(dest, entry.Name)
		if err != nil {
			return err
		}

		if entry.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}

		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}

		src, err := entry.Open()
		if err != nil {
			return err
		}

		// A zip stores a symlink as an entry whose CONTENT is the target path.
		// Written as a regular file it would produce a small text file with a
		// library's name in it - which links nothing and fails at load rather
		// than at extraction. Windows archives rarely carry these, but reading
		// one as a regular file is wrong wherever it happens.
		if entry.Mode()&os.ModeSymlink != 0 {
			name, readErr := io.ReadAll(io.LimitReader(src, 4096))
			src.Close()
			if readErr != nil {
				return readErr
			}
			if err := writeLink(dest, target, string(name)); err != nil {
				return err
			}
			continue
		}

		err = writeFile(target, src, entry.Mode())
		src.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func extractTarGz(archivePath, dest string) error {
	file, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer file.Close()

	gz, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gz.Close()

	reader := tar.NewReader(gz)
	hardLinks := make([]pendingHardLink, 0)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			return resolveHardLinks(hardLinks)
		}
		if err != nil {
			return err
		}

		target, err := safeJoin(dest, header.Name)
		if err != nil {
			return err
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := writeFile(target, reader, os.FileMode(header.Mode)); err != nil {
				return err
			}
		case tar.TypeSymlink:
			// Symlinks are REQUIRED, not merely tolerated. A llama.cpp release
			// ships its shared libraries as SONAME chains -
			// libllama.so -> libllama.so.0 -> libllama.so.0.0.10107 - and the
			// server binary links against the middle name. Skipping the links
			// produces a directory that verifies, extracts, and then fails at
			// exec with "cannot open shared object file", which is a long way
			// from the archive that caused it.
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := writeLink(dest, target, header.Linkname); err != nil {
				return err
			}
		case tar.TypeLink:
			// Hard-link sources are archive-root-relative. Validation happens
			// now, but creation is deferred until regular files exist so
			// forward references and chains resolve correctly.
			source, err := safeJoin(dest, header.Linkname)
			if err != nil {
				return err
			}
			if source == target {
				return fmt.Errorf("%w: hard link %q targets itself", ErrUnsafePath, header.Name)
			}
			hardLinks = append(hardLinks, pendingHardLink{target: target, source: source})
		default:
			// Devices, FIFOs and sockets are still skipped: nothing this
			// downloader fetches needs one, and creating them is a privilege
			// question rather than an extraction one.
			continue
		}
	}
}

type pendingHardLink struct {
	target string
	source string
}

func resolveHardLinks(links []pendingHardLink) error {
	for len(links) > 0 {
		unresolved := links[:0]
		resolved := 0
		for _, link := range links {
			info, err := os.Lstat(link.source)
			if os.IsNotExist(err) {
				unresolved = append(unresolved, link)
				continue
			}
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("%w: hard-link source %q is not a regular file",
					ErrUnsafePath, link.source)
			}
			if err := os.MkdirAll(filepath.Dir(link.target), 0o755); err != nil {
				return err
			}
			if err := os.Remove(link.target); err != nil && !os.IsNotExist(err) {
				return err
			}
			if err := os.Link(link.source, link.target); err != nil {
				return err
			}
			resolved++
		}
		if resolved == 0 {
			return fmt.Errorf("%w: missing or cyclic hard-link target %q",
				ErrUnsafePath, unresolved[0].source)
		}
		links = unresolved
	}
	return nil
}

func writeFile(path string, src io.Reader, mode os.FileMode) error {
	if mode == 0 {
		mode = 0o644
	}
	// Never executable from an archive unless the archive says so, and never
	// group- or world-writable.
	mode &= 0o755

	out, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, src); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	// OpenFile applies mode only when creating. Explicit chmod also preserves
	// archive mode bits when a later entry replaces an earlier path.
	return os.Chmod(path, mode)
}

func validatePublicRedirect(req *http.Request) error {
	host := req.URL.Hostname()
	if host == "" {
		return fmt.Errorf("%w: empty host", ErrUnsafeRedirect)
	}
	addresses, err := net.DefaultResolver.LookupIPAddr(req.Context(), host)
	if err != nil {
		return fmt.Errorf("%w: resolve %q: %v", ErrUnsafeRedirect, host, err)
	}
	if len(addresses) == 0 {
		return fmt.Errorf("%w: %q has no addresses", ErrUnsafeRedirect, host)
	}
	for _, address := range addresses {
		ip := address.IP
		if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() ||
			ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
			return fmt.Errorf("%w: %s resolves to %s", ErrUnsafeRedirect, host, ip)
		}
	}
	return nil
}

func treeDigest(root string) (string, error) {
	hash := sha256.New()
	err := filepath.WalkDir(root, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, current)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if relative == "." || relative == integrityFile {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		io.WriteString(hash, strconv.Itoa(len(relative)))
		io.WriteString(hash, ":")
		io.WriteString(hash, relative)
		io.WriteString(hash, "\x00")
		io.WriteString(hash, info.Mode().Type().String())
		io.WriteString(hash, ":")
		io.WriteString(hash, info.Mode().Perm().String())
		io.WriteString(hash, "\x00")
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(current)
			if err != nil {
				return err
			}
			io.WriteString(hash, target)
		case info.Mode().IsRegular():
			file, err := os.Open(current)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(hash, file)
			closeErr := file.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		}
		io.WriteString(hash, "\x00")
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// writeLink creates a symlink whose target is checked to stay inside dest.
//
// The check is NOT safeJoin on the link name: a symlink target resolves against
// the directory holding the LINK, not against the archive root, so
// "libllama.so.0" inside "llama-b10107/" means "llama-b10107/libllama.so.0".
// Resolving it as a root-relative path would accept a link that escapes and
// reject the ordinary same-directory chain every one of these archives uses.
//
// An escaping target is refused rather than skipped. Skipping is what this code
// did before, and it is how a broken extraction reached exec time looking fine.
func writeLink(dest, linkPath, name string) error {
	cleaned, err := cleanArchivePath(name, false)
	if err != nil {
		return fmt.Errorf("%w: symlink target %q", err, name)
	}

	root, err := filepath.Abs(dest)
	if err != nil {
		return err
	}
	target := filepath.Clean(filepath.Join(filepath.Dir(linkPath), filepath.FromSlash(cleaned)))
	if !pathWithin(root, target) {
		return fmt.Errorf("%w: symlink %q points outside the archive",
			ErrUnsafePath, name)
	}

	// A re-extraction over a surviving link would otherwise fail with EEXIST.
	if err := os.Remove(linkPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	// Preserve a relative target so the extracted directory remains movable.
	return os.Symlink(filepath.FromSlash(cleaned), linkPath)
}

// safeJoin rejects an archive entry that would escape the destination.
//
// The threat is concrete and old: an entry named "../../.ssh/authorized_keys"
// would otherwise be written wherever the path led.
func safeJoin(dest, name string) (string, error) {
	cleaned, err := cleanArchivePath(name, true)
	if err != nil {
		return "", err
	}

	root, err := filepath.Abs(dest)
	if err != nil {
		return "", err
	}
	target := filepath.Clean(filepath.Join(root, filepath.FromSlash(cleaned)))
	if !pathWithin(root, target) {
		return "", fmt.Errorf("%w: %q", ErrUnsafePath, name)
	}
	return target, nil
}

func cleanArchivePath(name string, rootRelative bool) (string, error) {
	if name == "" {
		return "", fmt.Errorf("%w: empty path", ErrUnsafePath)
	}
	normalized := strings.ReplaceAll(name, "\\", "/")
	if strings.HasPrefix(normalized, "/") || windowsVolumePath(normalized) {
		return "", fmt.Errorf("%w: %q", ErrUnsafePath, name)
	}
	cleaned := path.Clean(normalized)
	if rootRelative && (cleaned == ".." || strings.HasPrefix(cleaned, "../")) {
		return "", fmt.Errorf("%w: %q", ErrUnsafePath, name)
	}
	return cleaned, nil
}

func windowsVolumePath(name string) bool {
	if strings.HasPrefix(name, "//") {
		return true
	}
	return len(name) >= 2 &&
		((name[0] >= 'a' && name[0] <= 'z') || (name[0] >= 'A' && name[0] <= 'Z')) &&
		name[1] == ':'
}

func pathWithin(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil || filepath.IsAbs(rel) {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}
