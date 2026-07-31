package assets

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These downloads become native code loaded into the Stash process and model
// graphs it executes, so the checks below are the security boundary rather than
// hygiene. Stash's existing ffmpeg downloader has no checksum verification;
// this must not inherit that.

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// serveBytes returns a server handing out fixed content at /file.
func serveBytes(t *testing.T, content []byte) *httptest.Server {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(content)
	}))
	t.Cleanup(server.Close)
	return server
}

func newDownloader(t *testing.T, server *httptest.Server) *Downloader {
	t.Helper()
	return &Downloader{Dir: t.TempDir(), HTTP: server.Client()}
}

func TestFetchVerifiesTheChecksum(t *testing.T) {
	content := []byte("model weights go here")
	server := serveBytes(t, content)
	d := newDownloader(t, server)

	asset := Asset{Name: "model.onnx", URL: server.URL + "/file", SHA256: digest(content)}

	path, err := d.Fetch(context.Background(), asset, nil)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("downloaded content differs from what was served")
	}

	// Already installed: a second call must not re-download.
	if !d.Installed(asset) {
		t.Error("a freshly downloaded asset does not report as installed")
	}
}

// A file that is not what was published must never reach the model directory.
func TestFetchRejectsATamperedFile(t *testing.T) {
	server := serveBytes(t, []byte("something else entirely"))
	d := newDownloader(t, server)

	asset := Asset{
		Name:   "model.onnx",
		URL:    server.URL + "/file",
		SHA256: digest([]byte("the expected content")),
	}

	_, err := d.Fetch(context.Background(), asset, nil)
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("Fetch = %v, want ErrChecksumMismatch", err)
	}
	if _, err := os.Stat(filepath.Join(d.Dir, "model.onnx")); !os.IsNotExist(err) {
		t.Error("a file that failed verification was installed anyway")
	}
}

// An asset with no published checksum is refused rather than warned about: it
// would be arbitrary code from a host the user did not choose.
func TestFetchRefusesAnUnverifiableAsset(t *testing.T) {
	server := serveBytes(t, []byte("x"))
	d := newDownloader(t, server)

	_, err := d.Fetch(context.Background(), Asset{Name: "m.onnx", URL: server.URL + "/file"}, nil)
	if err == nil {
		t.Fatal("an asset with no checksum was downloaded")
	}
	if !strings.Contains(err.Error(), "checksum") {
		t.Errorf("the error does not explain why: %v", err)
	}
}

func TestFetchRefusesPreExistingUnverifiableAsset(t *testing.T) {
	d := &Downloader{Dir: t.TempDir()}
	asset := Asset{Name: "llama-server", URL: "https://example.com/llama-server"}
	if err := os.WriteFile(d.Path(asset), []byte("not pinned"), 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := d.Fetch(context.Background(), asset, nil); err == nil {
		t.Fatal("a pre-existing checksum-empty executable was accepted")
	} else if !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("Fetch error = %v, want checksum refusal", err)
	}
}

func TestFetchRefusesPlaintextURLs(t *testing.T) {
	d := &Downloader{Dir: t.TempDir()}

	_, err := d.Fetch(context.Background(), Asset{
		Name: "m.onnx", URL: "http://example.com/model.onnx", SHA256: digest(nil),
	}, nil)
	if !errors.Is(err, ErrInsecureURL) {
		t.Errorf("Fetch = %v, want ErrInsecureURL", err)
	}

	_, err = d.Fetch(context.Background(), Asset{
		Name: "m.onnx", URL: "file:///etc/passwd", SHA256: digest(nil),
	}, nil)
	if err == nil {
		t.Error("a file:// URL was accepted")
	}
}

func TestFetchRejectsRedirectsToInternalHosts(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://127.0.0.1/private", http.StatusFound)
	}))
	defer server.Close()

	d := newDownloader(t, server)
	_, err := d.Fetch(context.Background(), Asset{
		Name: "model.onnx", URL: server.URL, SHA256: digest(nil),
	}, nil)
	if !errors.Is(err, ErrUnsafeRedirect) {
		t.Fatalf("Fetch = %v, want ErrUnsafeRedirect", err)
	}
}

func TestFetchEnforcesPublishedSize(t *testing.T) {
	content := []byte("12345")
	for _, size := range []int64{4, 6} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			server := serveBytes(t, content)
			d := newDownloader(t, server)
			_, err := d.Fetch(context.Background(), Asset{
				Name: "model.onnx", URL: server.URL + "/file",
				SHA256: digest(content), Size: size,
			}, nil)
			if !errors.Is(err, ErrSizeMismatch) {
				t.Fatalf("Fetch = %v, want ErrSizeMismatch", err)
			}
		})
	}
}

// A corrupted cache must be re-downloaded rather than loaded: a truncated model
// produces garbage embeddings rather than an error.
func TestInstalledRejectsACorruptedCache(t *testing.T) {
	content := []byte("model weights")
	server := serveBytes(t, content)
	d := newDownloader(t, server)

	asset := Asset{Name: "model.onnx", URL: server.URL + "/file", SHA256: digest(content)}
	path, err := d.Fetch(context.Background(), asset, nil)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte("corrupted"), 0o644); err != nil {
		t.Fatal(err)
	}
	if d.Installed(asset) {
		t.Error("a corrupted cache reported as installed")
	}

	// And re-fetching repairs it.
	if _, err := d.Fetch(context.Background(), asset, nil); err != nil {
		t.Fatalf("re-fetch: %v", err)
	}
	if !d.Installed(asset) {
		t.Error("re-fetching did not repair the cache")
	}
}

// buildTarGz assembles an archive from a name-to-content map.
func buildTarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	for name, content := range files {
		if err := tw.WriteHeader(&tar.Header{
			Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

type tarFixture struct {
	name, link, content string
	kind                byte
	mode                int64
}

func buildOrderedTarGz(t *testing.T, entries ...tarFixture) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, entry := range entries {
		mode := entry.mode
		if mode == 0 {
			mode = 0o644
		}
		header := &tar.Header{
			Name: entry.name, Linkname: entry.link, Mode: mode, Typeflag: entry.kind,
		}
		if entry.kind == tar.TypeReg {
			header.Size = int64(len(entry.content))
		}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if entry.kind == tar.TypeReg {
			if _, err := tw.Write([]byte(entry.content)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func fetchTarFixture(t *testing.T, archive []byte, extractPath string) (*Downloader, string, error) {
	t.Helper()
	server := serveBytes(t, archive)
	d := newDownloader(t, server)
	path, err := d.Fetch(context.Background(), Asset{
		Name: "fixture", URL: server.URL + "/file", SHA256: digest(archive),
		Archive: "tgz", ExtractPath: extractPath,
	}, nil)
	return d, path, err
}

func TestFetchExtractsATarGz(t *testing.T) {
	archive := buildTarGz(t, map[string]string{
		"onnxruntime-linux-x64-1.20.1/lib/libonnxruntime.so.1.20.1": "ELF...",
		"onnxruntime-linux-x64-1.20.1/README":                       "docs",
	})
	server := serveBytes(t, archive)
	d := newDownloader(t, server)

	asset := Asset{
		Name:        "onnxruntime",
		URL:         server.URL + "/file",
		SHA256:      digest(archive),
		Archive:     "tgz",
		ExtractPath: "onnxruntime-linux-x64-1.20.1/lib/libonnxruntime.so.1.20.1",
	}

	path, err := d.Fetch(context.Background(), asset, nil)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the extracted library is missing: %v", err)
	}
	if string(got) != "ELF..." {
		t.Errorf("extracted content = %q", got)
	}
}

func TestInstalledRejectsModifiedArchiveTree(t *testing.T) {
	archive := buildTarGz(t, map[string]string{"pkg/model": "verified"})
	server := serveBytes(t, archive)
	d := newDownloader(t, server)
	asset := Asset{
		Name: "archive", URL: server.URL + "/file", SHA256: digest(archive),
		Archive: "tgz", ExtractPath: "pkg/model",
	}
	path, err := d.Fetch(context.Background(), asset, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Installed(asset) {
		t.Fatal("fresh archive does not report installed")
	}
	if err := os.WriteFile(path, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if d.Installed(asset) {
		t.Fatal("modified extracted native artifact reports installed")
	}
}

func TestExtractResolvesForwardHardLinksAndChains(t *testing.T) {
	archive := buildOrderedTarGz(t,
		tarFixture{name: "pkg/final.so", link: "pkg/middle.so", kind: tar.TypeLink},
		tarFixture{name: "pkg/middle.so", link: "pkg/real.so", kind: tar.TypeLink},
		tarFixture{name: "pkg/real.so", content: "ELF", kind: tar.TypeReg},
	)
	d, path, err := fetchTarFixture(t, archive, "pkg/final.so")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	realInfo, err := os.Stat(filepath.Join(d.Dir, "fixture", "pkg", "real.so"))
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []string{
		path,
		filepath.Join(d.Dir, "fixture", "pkg", "middle.so"),
	} {
		info, err := os.Lstat(candidate)
		if err != nil {
			t.Fatal(err)
		}
		if !info.Mode().IsRegular() || !os.SameFile(realInfo, info) {
			t.Errorf("%s was not preserved as a hard link to real.so", candidate)
		}
	}
}

func TestExtractRefusesInvalidHardLinkSources(t *testing.T) {
	cases := []struct {
		name    string
		entries []tarFixture
	}{
		{
			name: "missing",
			entries: []tarFixture{
				{name: "pkg/link", link: "pkg/missing", kind: tar.TypeLink},
			},
		},
		{
			name: "cycle",
			entries: []tarFixture{
				{name: "pkg/a", link: "pkg/b", kind: tar.TypeLink},
				{name: "pkg/b", link: "pkg/a", kind: tar.TypeLink},
			},
		},
		{
			name: "symlink source",
			entries: []tarFixture{
				{name: "pkg/real", content: "data", kind: tar.TypeReg},
				{name: "pkg/source", link: "real", kind: tar.TypeSymlink},
				{name: "pkg/link", link: "pkg/source", kind: tar.TypeLink},
			},
		},
		{
			name: "directory source",
			entries: []tarFixture{
				{name: "pkg/source", kind: tar.TypeDir},
				{name: "pkg/link", link: "pkg/source", kind: tar.TypeLink},
			},
		},
		{
			name: "escaping source",
			entries: []tarFixture{
				{name: "pkg/link", link: "../outside", kind: tar.TypeLink},
			},
		},
		{
			name: "self reference",
			entries: []tarFixture{
				{name: "pkg/link", link: "pkg/link", kind: tar.TypeLink},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			archive := buildOrderedTarGz(t, tc.entries...)
			d, _, err := fetchTarFixture(t, archive, "pkg/link")
			if !errors.Is(err, ErrUnsafePath) {
				t.Fatalf("Fetch = %v, want ErrUnsafePath", err)
			}
			if _, statErr := os.Stat(filepath.Join(d.Dir, "fixture")); !os.IsNotExist(statErr) {
				t.Errorf("failed hard-link extraction left an installed tree: %v", statErr)
			}
		})
	}
}

func TestArchiveExecuteBitsSurviveAndBareDownloadsStayNonExecutable(t *testing.T) {
	archive := buildOrderedTarGz(t,
		tarFixture{name: "bin/llama-server", content: "#!/bin/true", kind: tar.TypeReg, mode: 0o755},
		tarFixture{name: "lib/data", content: "data", kind: tar.TypeReg, mode: 0o644},
	)
	d, executable, err := fetchTarFixture(t, archive, "bin/llama-server")
	if err != nil {
		t.Fatalf("Fetch archive: %v", err)
	}
	info, err := os.Stat(executable)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Errorf("archive executable mode = %o, want 755", got)
	}
	info, err = os.Stat(filepath.Join(d.Dir, "fixture", "lib", "data"))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o644 {
		t.Errorf("archive data mode = %o, want 644", got)
	}

	content := []byte("model")
	server := serveBytes(t, content)
	plain := newDownloader(t, server)
	path, err := plain.Fetch(context.Background(), Asset{
		Name: "model.gguf", URL: server.URL + "/file", SHA256: digest(content),
	}, nil)
	if err != nil {
		t.Fatalf("Fetch bare file: %v", err)
	}
	info, err = os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o644 {
		t.Errorf("bare download mode = %o, want 644", got)
	}
}

// The classic archive escape: an entry whose path climbs out of the
// destination. It must be refused rather than written wherever it points.
func TestExtractRefusesPathTraversal(t *testing.T) {
	archive := buildTarGz(t, map[string]string{
		"../../escaped.txt": "owned",
	})
	server := serveBytes(t, archive)
	d := newDownloader(t, server)

	asset := Asset{
		Name: "evil", URL: server.URL + "/file", SHA256: digest(archive),
		Archive: "tgz", ExtractPath: "anything",
	}

	_, err := d.Fetch(context.Background(), asset, nil)
	if !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("Fetch = %v, want ErrUnsafePath", err)
	}

	// And nothing landed outside.
	if _, err := os.Stat(filepath.Join(filepath.Dir(d.Dir), "escaped.txt")); err == nil {
		t.Fatal("an archive escaped its destination")
	}
}

func TestFailedExtractionLeavesNoInstalledTreeOrStaging(t *testing.T) {
	archive := buildTarGz(t, map[string]string{
		"partial/file":     "written before failure",
		"../../escape.txt": "rejected",
	})
	server := serveBytes(t, archive)
	d := newDownloader(t, server)
	asset := Asset{
		Name: "broken", URL: server.URL + "/file", SHA256: digest(archive),
		Archive: "tgz", ExtractPath: "partial/file",
	}

	if _, err := d.Fetch(context.Background(), asset, nil); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("Fetch = %v, want ErrUnsafePath", err)
	}
	if d.Installed(asset) {
		t.Fatal("a partial extraction reports as installed")
	}
	entries, err := os.ReadDir(d.Dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), "extract-") || entry.Name() == asset.Name {
			t.Errorf("failed extraction left %q behind", entry.Name())
		}
	}
}

func TestSafeJoinRejectsPortableEscapeForms(t *testing.T) {
	for _, name := range []string{
		"/absolute",
		`C:\outside`,
		"C:relative",
		`\\server\share\outside`,
		`..\outside`,
		`nested\..\..\outside`,
		"..",
		"a/../../b",
	} {
		if _, err := safeJoin(t.TempDir(), name); !errors.Is(err, ErrUnsafePath) {
			t.Errorf("safeJoin(%q) = %v, want ErrUnsafePath", name, err)
		}
	}

	dir := t.TempDir()
	want := filepath.Join(dir, "lib", "sub", "file.so")
	for _, name := range []string{"lib/sub/file.so", `lib\sub\file.so`} {
		got, err := safeJoin(dir, name)
		if err != nil {
			t.Fatalf("safeJoin(%q) rejected a legitimate nested path: %v", name, err)
		}
		if got != want {
			t.Errorf("safeJoin(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestZipExtractionAlsoRefusesTraversal(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("../escaped.dll")
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte("owned"))
	zw.Close()

	archive := buf.Bytes()
	server := serveBytes(t, archive)
	d := newDownloader(t, server)

	_, err = d.Fetch(context.Background(), Asset{
		Name: "evil", URL: server.URL + "/file", SHA256: digest(archive),
		Archive: "zip", ExtractPath: "x",
	}, nil)
	if !errors.Is(err, ErrUnsafePath) {
		t.Errorf("Fetch = %v, want ErrUnsafePath", err)
	}
}

// A cancelled download must stop promptly rather than at the end of a very
// large file, and must leave nothing behind.
func TestFetchHonoursCancellation(t *testing.T) {
	// Large enough that the download cannot finish before the cancel lands.
	big := bytes.Repeat([]byte("x"), 8<<20)
	server := serveBytes(t, big)
	d := newDownloader(t, server)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := d.Fetch(ctx, Asset{
		Name: "big.onnx", URL: server.URL + "/file", SHA256: digest(big),
	}, nil)
	if err == nil {
		t.Fatal("a cancelled download reported success")
	}

	entries, err := os.ReadDir(d.Dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".download-") {
			t.Errorf("a cancelled download left %q behind", entry.Name())
		}
	}
}

// Every catalog entry must state its licence: the user is being asked to accept
// it, and a licence nobody is shown is not consent.
func TestEveryCatalogEntryStatesItsLicence(t *testing.T) {
	for _, model := range Catalog() {
		if model.License == "" {
			t.Errorf("model %s has no licence", model.Name)
		}
		if model.LicenseURL == "" {
			t.Errorf("model %s has no licence URL", model.Name)
		}
		if model.Description == "" {
			t.Errorf("model %s has no description", model.Name)
		}
		if model.Role == "" {
			t.Errorf("model %s has no role", model.Name)
		}
	}
}

// InsightFace buffalo_l must stay out: its code is MIT but its weights are
// research-only, which is not a free licence.
func TestResearchOnlyWeightsAreNotInTheCatalog(t *testing.T) {
	for _, model := range Catalog() {
		lower := strings.ToLower(model.Name)
		if strings.Contains(lower, "buffalo") || strings.Contains(lower, "insightface") {
			t.Errorf("%s has research-only weights and must not be offered", model.Name)
		}
	}
}

// Every downloadable entry must carry a checksum, because Fetch refuses one
// without and an entry that cannot be fetched is worse than one that says so.
func TestDownloadableEntriesAreVerifiable(t *testing.T) {
	for _, model := range Catalog() {
		all := append([]Model{model}, model.Variants...)
		for _, m := range all {
			if m.URL == "" {
				// Describes something the user must export; must not claim to
				// be downloadable.
				if m.Downloadable() {
					t.Errorf("%s has no URL but reports itself downloadable", m.Name)
				}
				continue
			}
			if m.SHA256 == "" {
				t.Errorf("%s has a URL but no checksum; Fetch would refuse it", m.Name)
			}
			if len(m.SHA256) != 64 {
				t.Errorf("%s has a %d-character checksum, want 64 hex", m.Name, len(m.SHA256))
			}
			if m.Size <= 0 {
				t.Errorf("%s declares no size", m.Name)
			}
			if err := validateURL(m.URL); err != nil {
				t.Errorf("%s: %v", m.Name, err)
			}
		}
	}
}

// The input convention is three independent ways to be silently wrong, so every
// entry must state all three rather than inheriting a zero value.
func TestEveryModelStatesItsInputConvention(t *testing.T) {
	for _, model := range Catalog() {
		if model.Role == RoleAudio {
			// Audio has no channel order or pixel range.
			continue
		}
		if model.Channels != ChannelRGB && model.Channels != ChannelBGR {
			t.Errorf("%s declares channel order %q", model.Name, model.Channels)
		}
		if model.Pixels != PixelUnit && model.Pixels != PixelRaw {
			t.Errorf("%s declares pixel range %q", model.Name, model.Pixels)
		}
		if model.InputSize <= 0 {
			t.Errorf("%s declares no input size", model.Name)
		}
		// A model normalising to [0,1] needs a usable standard deviation; zero
		// would divide by zero and fill the tensor with infinities.
		if model.Pixels == PixelUnit {
			for i, std := range model.Std {
				if std == 0 {
					t.Errorf("%s has a zero standard deviation on channel %d", model.Name, i)
				}
			}
		}
	}
}

// The 1.28.0 runtime pin is set by the binding's ORT API level, while Pascal
// CUDA support ended before any runtime that can satisfy that binding.
// The runtime pin is set by the binding: onnxruntime_go compiles against
// ORT_API_VERSION 26 and a library implementing less returns NULL from
// OrtGetApiBase rather than degrading. This asserts the direction of that
// constraint, which is the one that is easy to get backwards - the instinct is
// "pin low for compatibility", and pinning low is precisely what breaks it.
func TestRuntimeVersionSatisfiesTheBinding(t *testing.T) {
	if DefaultRuntimeVersion != "1.28.0" {
		t.Errorf("DefaultRuntimeVersion = %s; 1.28.0 is the release the binding was verified "+
			"against. Changing it needs TestRuntimeIsActuallyLoadable re-run, not just new "+
			"checksums: too old and OrtGetApiBase returns NULL for API 26.", DefaultRuntimeVersion)
	}

	// Pascal CUDA is unreachable rather than merely unchosen, and the assertion
	// says so: the last release serving compute 6.1 is OLDER than the oldest
	// this binding can load, so a GPU path needs onnxruntime_go downgraded too.
	if LastPascalRuntimeVersion >= DefaultRuntimeVersion {
		t.Errorf("LastPascalRuntimeVersion %s is no longer below the pin %s; if a runtime "+
			"can serve both Pascal and API 26, the CUDA provider is worth reconsidering",
			LastPascalRuntimeVersion, DefaultRuntimeVersion)
	}

	asset, err := RuntimeAsset("")
	if err != nil {
		t.Skipf("no runtime for this platform: %v", err)
	}
	if !strings.Contains(asset.URL, DefaultRuntimeVersion) {
		t.Errorf("the runtime URL does not carry the pinned version: %s", asset.URL)
	}
}

// THE bug this replaced: Fetch refuses an asset with no checksum, so an
// unpinned RuntimeAsset is not merely unverified - the native path cannot be
// installed at all. Asserted for every platform, not just this one.
func TestRuntimeIsActuallyFetchable(t *testing.T) {
	asset, err := RuntimeAsset("")
	if err != nil {
		t.Skipf("no runtime for this platform: %v", err)
	}

	if asset.SHA256 == "" {
		t.Fatal("the runtime carries no checksum; Fetch would refuse it and the native path could never install")
	}
	if len(asset.SHA256) != 64 {
		t.Errorf("checksum is %d characters, want 64 hex", len(asset.SHA256))
	}
	if asset.Size <= 0 {
		t.Error("the runtime declares no size")
	}
	if asset.ExtractPath == "" {
		t.Error("the runtime does not name the library inside its archive")
	}

	// Every platform must be pinned, or a user on one of them hits the same
	// un-installable state this test exists to prevent. osx-arm64 rather than
	// osx-universal2: the universal build was discontinued after 1.22.x, so an
	// Intel Mac has no published archive and RuntimeAsset errors for it.
	for _, platform := range []string{"linux-x64", "linux-aarch64", "osx-arm64", "win-x64"} {
		if _, ok := runtimeChecksums[platform]; !ok {
			t.Errorf("no checksum pinned for %s", platform)
		}
	}
	if len(runtimeChecksums) != 4 {
		t.Errorf("runtimeChecksums has %d entries; a stale platform key would pin a "+
			"checksum no RuntimeAsset can ask for", len(runtimeChecksums))
	}
}

// A version other than the pinned one must come back unverifiable rather than
// silently unverified: better a clear refusal than an unchecked native library.
func TestUnpinnedRuntimeVersionIsRefused(t *testing.T) {
	asset, err := RuntimeAsset("1.19.0")
	if err != nil {
		t.Skipf("no runtime for this platform: %v", err)
	}
	if asset.SHA256 != "" {
		t.Error("an unpinned version came back with a checksum it cannot have")
	}

	d := &Downloader{Dir: t.TempDir()}
	if _, err := d.Fetch(context.Background(), asset, nil); err == nil {
		t.Error("Fetch accepted an unpinned runtime version")
	}
}

// An embedding model with no declared dimension would silently produce
// mis-shaped output tensors.
func TestEmbeddingModelsDeclareTheirDimension(t *testing.T) {
	for _, model := range ModelsForRole(RoleEmbedding) {
		if model.Dim <= 0 {
			t.Errorf("embedding model %s declares no dimension", model.Name)
		}
		if model.InputSize <= 0 {
			t.Errorf("embedding model %s declares no input size", model.Name)
		}
		if len(model.Inputs) == 0 || len(model.Outputs) == 0 {
			t.Errorf("model %s does not name its tensors", model.Name)
		}
	}
}

// The runtime is the one artefact that is dlopen'd as native code, so its
// download must be HTTPS and per-platform.
func TestRuntimeAssetIsHTTPS(t *testing.T) {
	asset, err := RuntimeAsset("")
	if err != nil {
		t.Skipf("no runtime published for this platform: %v", err)
	}
	if !strings.HasPrefix(asset.URL, "https://") {
		t.Errorf("runtime URL is not HTTPS: %s", asset.URL)
	}
	if asset.ExtractPath == "" {
		t.Error("the runtime asset does not name the library inside its archive")
	}
	if err := validateURL(asset.URL); err != nil {
		t.Errorf("the runtime URL fails validation: %v", err)
	}
}

// A paired model is only usable when BOTH halves are present, so a pair with
// one verifiable half must not report itself downloadable: that produces a
// half-installed model failing at load time rather than at download time.
func TestPairNeedsBothHalves(t *testing.T) {
	content := []byte("gguf")
	server := serveBytes(t, content)
	d := newDownloader(t, server)

	full := Pair{
		Name:      "demo",
		Primary:   Asset{Name: "model.gguf", URL: server.URL + "/m", SHA256: digest(content)},
		Companion: Asset{Name: "mmproj.gguf", URL: server.URL + "/p", SHA256: digest(content)},
	}
	if !full.Downloadable() {
		t.Fatal("a fully-specified pair reports itself undownloadable")
	}

	half := full
	half.Companion.SHA256 = ""
	if half.Downloadable() {
		t.Error("a pair with an unverifiable projector reports itself downloadable")
	}
	if _, _, err := d.FetchPair(context.Background(), half, nil); err == nil {
		t.Error("FetchPair accepted a pair with no projector checksum")
	}

	primary, companion, err := d.FetchPair(context.Background(), full, nil)
	if err != nil {
		t.Fatalf("FetchPair: %v", err)
	}
	for _, path := range []string{primary, companion} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s was not written: %v", path, err)
		}
	}
	if !d.PairInstalled(full) {
		t.Error("a freshly fetched pair does not report as installed")
	}

	// Removing one half must make the pair incomplete again, or a partial
	// install would be treated as ready.
	if err := os.Remove(companion); err != nil {
		t.Fatal(err)
	}
	if d.PairInstalled(full) {
		t.Error("a pair with a missing projector reports as installed")
	}
}

// The default embedder is a NAME, so reordering the catalog cannot change which
// model heads are trained against. A head is a set of weights over one model's
// 768-dimensional output; run it on another model's embeddings and it returns
// confident, plausible, meaningless scores rather than an error.
func TestDefaultEmbedderIsPinned(t *testing.T) {
	model, ok := FindModel(DefaultEmbedder)
	if !ok {
		t.Fatalf("DefaultEmbedder %q is not in the catalog", DefaultEmbedder)
	}
	if model.Role != RoleEmbedding {
		t.Errorf("the default embedder has role %q, want %q", model.Role, RoleEmbedding)
	}
	if model.Dim != 768 || model.InputSize != 224 {
		t.Errorf("the embedding contract moved: %d-d at %d px, want 768 at 224. "+
			"Every head trained against the old contract is invalid.", model.Dim, model.InputSize)
	}
	if !model.Downloadable() {
		t.Error("the default embedder cannot be downloaded; the native path would have no embedder")
	}
}

// buildTarGzWithLinks assembles an archive containing regular files and
// symlinks, so the SONAME chains a shared-library release depends on can be
// exercised directly.
func buildTarGzWithLinks(t *testing.T, files map[string]string, links map[string]string) []byte {
	t.Helper()

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	for name, content := range files {
		if err := tw.WriteHeader(&tar.Header{
			Name: name, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	for name, target := range links {
		if err := tw.WriteHeader(&tar.Header{
			Name: name, Linkname: target, Mode: 0o777, Typeflag: tar.TypeSymlink,
		}); err != nil {
			t.Fatal(err)
		}
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

// A shared-library release ships SONAME chains, and the binary links against a
// name in the middle of one. Dropping the links yields a directory that
// verifies, extracts, and then fails at exec with "cannot open shared object
// file" - the failure this test exists to prevent.
func TestExtractPreservesSymlinkChains(t *testing.T) {
	archive := buildTarGzWithLinks(t,
		map[string]string{
			"llama-b1/libllama.so.0.0.1": "ELF...",
			"llama-b1/llama-server":      "#!/bin/true",
		},
		map[string]string{
			"llama-b1/libllama.so.0": "libllama.so.0.0.1",
			"llama-b1/libllama.so":   "libllama.so.0",
		})

	server := serveBytes(t, archive)
	d := newDownloader(t, server)

	asset := Asset{
		Name: "llama", URL: server.URL + "/file", SHA256: digest(archive),
		Archive: "tgz", ExtractPath: "llama-b1/llama-server",
	}

	path, err := d.Fetch(context.Background(), asset, nil)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	dir := filepath.Dir(path)
	for _, link := range []string{"libllama.so.0", "libllama.so"} {
		full := filepath.Join(dir, link)
		info, err := os.Lstat(full)
		if err != nil {
			t.Fatalf("%s was not created; the SONAME chain is broken: %v", link, err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			t.Errorf("%s is a regular file, not a symlink", link)
		}
	}

	// The whole chain must resolve to the real library, which is what the
	// dynamic loader actually does.
	content, err := os.ReadFile(filepath.Join(dir, "libllama.so"))
	if err != nil {
		t.Fatalf("the symlink chain does not resolve: %v", err)
	}
	if string(content) != "ELF..." {
		t.Errorf("resolved content = %q, want the real library", content)
	}

	// And the binary kept its executable bit, or it cannot be spawned.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&0o111 == 0 {
		t.Errorf("the server binary is not executable (mode %v)", info.Mode())
	}
}

// A symlink target is resolved against the LINK's directory, so an escaping
// target must be refused - and a same-directory one must not be.
func TestExtractRefusesEscapingSymlinks(t *testing.T) {
	cases := []struct {
		name   string
		target string
	}{
		{"empty target", ""},
		{"relative escape", "../../../etc/passwd"},
		{"backslash escape", `..\..\outside.so`},
		{"absolute target", "/etc/passwd"},
		{"drive absolute", `C:\outside`},
		{"drive relative", "C:relative"},
		{"UNC target", `\\server\share\outside`},
		// From "pkg/evil.so" this lands exactly one level above the
		// destination. Note "../outside.so" would NOT escape from that depth -
		// it resolves to dest/outside.so - and is correctly allowed.
		{"one level past the root", "../../outside.so"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			archive := buildTarGzWithLinks(t,
				map[string]string{"pkg/real.so": "ELF"},
				map[string]string{"pkg/evil.so": tc.target})

			server := serveBytes(t, archive)
			d := newDownloader(t, server)

			_, err := d.Fetch(context.Background(), Asset{
				Name: "evil", URL: server.URL + "/file", SHA256: digest(archive),
				Archive: "tgz", ExtractPath: "pkg/real.so",
			}, nil)
			if !errors.Is(err, ErrUnsafePath) {
				t.Fatalf("Fetch = %v, want ErrUnsafePath", err)
			}
		})
	}
}

// The links these archives actually use are same-directory and must survive the
// escape check, which is the case a root-relative validation would wrongly
// reject.
func TestSymlinkTargetResolvesAgainstItsOwnDirectory(t *testing.T) {
	dest := t.TempDir()
	linkPath := filepath.Join(dest, "nested", "libfoo.so")
	if err := os.MkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
		t.Fatal(err)
	}

	// "libfoo.so.1" beside the link means nested/libfoo.so.1, which is inside.
	if err := writeLink(dest, linkPath, "libfoo.so.1"); err != nil {
		t.Fatalf("a same-directory link was refused: %v", err)
	}
	got, err := os.Readlink(linkPath)
	if err != nil {
		t.Fatal(err)
	}
	// The link must stay RELATIVE, so the directory can be moved or renamed.
	if got != "libfoo.so.1" {
		t.Errorf("link target = %q, want the original relative name", got)
	}

	// Same spelling, one level up, now escapes.
	if err := writeLink(dest, filepath.Join(dest, "libbar.so"), "../escaped.so"); !errors.Is(err, ErrUnsafePath) {
		t.Errorf("writeLink = %v, want ErrUnsafePath", err)
	}
}

// The server binary is spawned as a process, so an unverified one is worse than
// an unverified library: it is arbitrary code executed directly.
func TestServerAssetIsPinnedAndFetchable(t *testing.T) {
	asset, err := ServerAsset("")
	if err != nil {
		t.Skipf("no llama.cpp build for this platform: %v", err)
	}

	if asset.SHA256 == "" {
		t.Fatal("the server carries no checksum; Fetch would refuse it and the VLM path could never install")
	}
	if len(asset.SHA256) != 64 || asset.Size <= 0 {
		t.Errorf("bad pin: %d-char checksum, size %d", len(asset.SHA256), asset.Size)
	}
	if asset.Archive == "" {
		t.Error("the server must arrive as an archive: a plain download is chmod 0644 and " +
			"could not be executed, and it would leave the shared libraries behind")
	}
	if !strings.Contains(asset.URL, DefaultServerVersion) {
		t.Errorf("the URL does not carry the pinned version: %s", asset.URL)
	}

	// Every platform pinned, or a user on one of them cannot install at all.
	for _, platform := range []string{
		"ubuntu-x64", "ubuntu-vulkan-x64", "ubuntu-arm64",
		"macos-arm64", "macos-x64", "win-cpu-x64",
	} {
		if _, ok := serverChecksums[platform]; !ok {
			t.Errorf("no checksum pinned for %s", platform)
		}
	}
	if len(serverChecksums) != 6 {
		t.Errorf("serverChecksums has %d entries; a stale key pins something "+
			"no ServerAsset can request", len(serverChecksums))
	}
}

func TestEveryServerPlatformCarriesCompleteMetadata(t *testing.T) {
	platforms := []struct {
		name, archive, binary string
	}{
		{"ubuntu-x64", "tgz", "llama-server"},
		{"ubuntu-vulkan-x64", "tgz", "llama-server"},
		{"ubuntu-arm64", "tgz", "llama-server"},
		{"macos-arm64", "tgz", "llama-server"},
		{"macos-x64", "tgz", "llama-server"},
		{"win-cpu-x64", "zip", "llama-server.exe"},
	}
	for _, platform := range platforms {
		t.Run(platform.name, func(t *testing.T) {
			asset, err := serverAssetFor(
				DefaultServerVersion, platform.name, platform.archive, platform.binary)
			if err != nil {
				t.Fatal(err)
			}
			if len(asset.SHA256) != 64 || asset.Size <= 0 {
				t.Errorf("incomplete integrity metadata: checksum=%d bytes, size=%d",
					len(asset.SHA256), asset.Size)
			}
			if asset.License == "" || asset.LicenseURL == "" {
				t.Error("server asset has incomplete licence metadata")
			}
			if asset.Name == "" || asset.ExtractPath == "" {
				t.Error("server asset has no resolvable installed name")
			}
		})
	}
}

// The Windows zip has no top-level directory while the Unix tarballs unpack into
// llama-<version>/. Asserted because the mistake surfaces only at the END of a
// download, as "archive does not contain".
func TestServerExtractPathMatchesTheArchiveLayout(t *testing.T) {
	asset, err := ServerAsset("")
	if err != nil {
		t.Skip("no build for this platform")
	}

	if asset.Archive == "zip" {
		if strings.Contains(asset.ExtractPath, "/") {
			t.Errorf("the Windows zip is flat, but ExtractPath is %q", asset.ExtractPath)
		}
		if !strings.HasSuffix(asset.ExtractPath, ".exe") {
			t.Errorf("ExtractPath %q does not name an .exe", asset.ExtractPath)
		}
		return
	}

	want := "llama-" + DefaultServerVersion + "/llama-server"
	if asset.ExtractPath != want {
		t.Errorf("ExtractPath = %q, want %q", asset.ExtractPath, want)
	}
}

func TestUnpinnedServerVersionIsRefused(t *testing.T) {
	asset, err := ServerAsset("b1")
	if err != nil {
		t.Skip("no build for this platform")
	}
	if asset.SHA256 != "" {
		t.Error("an unpinned version came back with a checksum it cannot have")
	}

	d := &Downloader{Dir: t.TempDir()}
	if _, err := d.Fetch(context.Background(), asset, nil); err == nil {
		t.Error("Fetch accepted an unpinned server build")
	}
}
