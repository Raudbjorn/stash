package manager

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stashapp/stash/internal/manager/config"
	"github.com/stashapp/stash/pkg/markersync"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/sqlite"
	"github.com/stashapp/stash/pkg/txn"

	// register custom migrations run by db.Open
	_ "github.com/stashapp/stash/pkg/sqlite/migrations"
)

var initConfigOnce sync.Once

// newTestRepository spins up a fresh, isolated on-disk sqlite database (schema
// created by db.Open's migrations) and returns its repository. The database is
// closed and removed on test cleanup.
func newTestRepository(t testing.TB) models.Repository {
	t.Helper()

	initConfigOnce.Do(func() {
		// some migrations require an initialised (empty) config
		_ = config.InitializeEmpty()
	})

	f, err := os.CreateTemp(t.TempDir(), "*.sqlite")
	if err != nil {
		t.Fatalf("creating temp db file: %v", err)
	}
	dbFile := f.Name()
	_ = f.Close()

	db := sqlite.NewDatabase()
	db.SetBlobStoreOptions(sqlite.BlobStoreOptions{UseDatabase: true})
	if err := db.Open(dbFile); err != nil {
		t.Fatalf("opening db: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})

	return db.Repository()
}

// fakeSource is a markersync.Source that returns canned candidates. It is used
// to prove the DB-backed markerWriter + tagResolver + markersync.Apply pipeline
// without any network access.
type fakeSource struct {
	name       string
	enabled    bool
	candidates []markersync.MarkerCandidate
}

func (s *fakeSource) Name() string  { return s.name }
func (s *fakeSource) Enabled() bool { return s.enabled }
func (s *fakeSource) FetchMarkers(_ context.Context, _ markersync.SceneIdentity) ([]markersync.MarkerCandidate, error) {
	return s.candidates, nil
}

func ptrFloat(f float64) *float64 { return &f }

func ptrInt(i int) *int { return &i }

// fakeExtraURLProvider is a markersync.ExtraURLProvider returning canned URLs.
type fakeExtraURLProvider struct{ urls []string }

func (f *fakeExtraURLProvider) FetchExtraURLs(_ context.Context, _ markersync.SceneIdentity) ([]string, error) {
	return f.urls, nil
}

// fakeGalleryProvider is a markersync.GalleryProvider returning canned refs.
type fakeGalleryProvider struct{ galleries []markersync.GalleryRef }

func (f *fakeGalleryProvider) FetchGalleries(_ context.Context, _ markersync.SceneIdentity) ([]markersync.GalleryRef, error) {
	return f.galleries, nil
}

// fakeGroupProvider is a markersync.GroupProvider returning canned refs.
type fakeGroupProvider struct{ groups []markersync.GroupRef }

func (f *fakeGroupProvider) FetchGroups(_ context.Context, _ markersync.SceneIdentity) ([]markersync.GroupRef, error) {
	return f.groups, nil
}

// TestMarkerSyncApplyExtraURLs proves the URL-merge apply: a scene starting with
// one URL, run against a provider that returns that URL plus a new one, ends up
// with both URLs merged and de-duplicated (order preserved).
func TestMarkerSyncApplyExtraURLs(t *testing.T) {
	r := newTestRepository(t)
	ctx := context.Background()

	var sceneID int
	if err := txn.WithTxn(ctx, r.TxnManager, func(ctx context.Context) error {
		sc := models.NewScene()
		sc.Title = "url merge scene"
		sc.URLs = models.NewRelatedStrings([]string{"https://existing.example/a"})
		if err := r.Scene.Create(ctx, &sc, nil); err != nil {
			return err
		}
		sceneID = sc.ID
		return nil
	}); err != nil {
		t.Fatalf("creating scene: %v", err)
	}

	task := &markerSyncTask{scene: &models.Scene{ID: sceneID}, syncURLs: true}
	p := &fakeExtraURLProvider{urls: []string{
		"https://existing.example/a", // duplicate, must not be added twice
		"https://new.example/b",      // new
	}}

	task.applyExtraURLs(ctx, r, "fake", p, markersync.SceneIdentity{})

	var got []string
	if err := txn.WithReadTxn(ctx, r.TxnManager, func(ctx context.Context) error {
		sc, err := r.Scene.Find(ctx, sceneID)
		if err != nil {
			return err
		}
		if err := sc.LoadURLs(ctx, r.Scene); err != nil {
			return err
		}
		got = sc.URLs.List()
		return nil
	}); err != nil {
		t.Fatalf("loading urls: %v", err)
	}

	want := []string{"https://existing.example/a", "https://new.example/b"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("merged urls = %v, want %v", got, want)
	}
}

// TestMarkerSyncApplyGroups proves the group create+attach path and its
// idempotency: a provider group is created, linked to the scene with the right
// scene index, and a re-run matches the existing group by URL (no duplicate).
func TestMarkerSyncApplyGroups(t *testing.T) {
	r := newTestRepository(t)
	ctx := context.Background()

	var sceneID int
	if err := txn.WithTxn(ctx, r.TxnManager, func(ctx context.Context) error {
		sc := models.NewScene()
		sc.Title = "group attach scene"
		if err := r.Scene.Create(ctx, &sc, nil); err != nil {
			return err
		}
		sceneID = sc.ID
		return nil
	}); err != nil {
		t.Fatalf("creating scene: %v", err)
	}

	task := &markerSyncTask{scene: &models.Scene{ID: sceneID}, syncGroups: true}
	p := &fakeGroupProvider{groups: []markersync.GroupRef{{
		ExternalID: "42",
		Name:       "The Collection",
		Synopsis:   "a synopsis",
		Date:       "2021-05-06",
		URLs:       []string{"https://timestamp.trade/movie/42"},
		SceneIndex: ptrInt(3),
	}}}

	// group count helper
	groupCount := func() int {
		t.Helper()
		var n int
		if err := txn.WithReadTxn(ctx, r.TxnManager, func(ctx context.Context) error {
			var err error
			n, err = r.Group.Count(ctx)
			return err
		}); err != nil {
			t.Fatalf("counting groups: %v", err)
		}
		return n
	}

	// scene's attached groups helper
	sceneGroups := func() []models.GroupsScenes {
		t.Helper()
		var gs []models.GroupsScenes
		if err := txn.WithReadTxn(ctx, r.TxnManager, func(ctx context.Context) error {
			sc, err := r.Scene.Find(ctx, sceneID)
			if err != nil {
				return err
			}
			if err := sc.LoadGroups(ctx, r.Scene); err != nil {
				return err
			}
			gs = sc.Groups.List()
			return nil
		}); err != nil {
			t.Fatalf("loading scene groups: %v", err)
		}
		return gs
	}

	// --- Pass 1: create + attach ---
	task.applyGroups(ctx, r, "fake", p, markersync.SceneIdentity{})

	if n := groupCount(); n != 1 {
		t.Fatalf("pass 1: group count = %d, want 1", n)
	}
	gs := sceneGroups()
	if len(gs) != 1 {
		t.Fatalf("pass 1: scene groups = %+v, want 1", gs)
	}
	if gs[0].SceneIndex == nil || *gs[0].SceneIndex != 3 {
		t.Fatalf("pass 1: scene index = %v, want 3", gs[0].SceneIndex)
	}

	// verify created group fields (name + parsed date)
	if err := txn.WithReadTxn(ctx, r.TxnManager, func(ctx context.Context) error {
		g, err := r.Group.Find(ctx, gs[0].GroupID)
		if err != nil {
			return err
		}
		if g.Name != "The Collection" {
			t.Fatalf("group name = %q, want The Collection", g.Name)
		}
		if g.Synopsis != "a synopsis" {
			t.Fatalf("group synopsis = %q, want 'a synopsis'", g.Synopsis)
		}
		if g.Date == nil || g.Date.String() != "2021-05-06" {
			t.Fatalf("group date = %v, want 2021-05-06", g.Date)
		}
		return nil
	}); err != nil {
		t.Fatalf("verifying group: %v", err)
	}

	// --- Pass 2: idempotent re-run (matched by URL, not duplicated) ---
	task.applyGroups(ctx, r, "fake", p, markersync.SceneIdentity{})

	if n := groupCount(); n != 1 {
		t.Fatalf("pass 2: group count = %d, want 1 (matched by URL, no duplicate)", n)
	}
	if gs := sceneGroups(); len(gs) != 1 {
		t.Fatalf("pass 2: scene groups = %+v, want 1 (no duplicate attach)", gs)
	}
}

// TestMarkerSyncApplyGalleries proves the md5 -> FindByChecksum -> attach path:
// a gallery with a known file checksum is linked to the scene and has its URLs
// back-filled from the provider ref (since it started with none).
func TestMarkerSyncApplyGalleries(t *testing.T) {
	r := newTestRepository(t)
	ctx := context.Background()

	const md5 = "deadbeefdeadbeefdeadbeefdeadbeef"

	var (
		sceneID   int
		galleryID int
	)
	if err := txn.WithTxn(ctx, r.TxnManager, func(ctx context.Context) error {
		sc := models.NewScene()
		sc.Title = "gallery attach scene"
		if err := r.Scene.Create(ctx, &sc, nil); err != nil {
			return err
		}
		sceneID = sc.ID

		// folder -> file (with md5 fingerprint) -> gallery
		folder := models.Folder{
			Path:     "/marker_sync_test/galleries",
			DirEntry: models.DirEntry{ModTime: time.Now()},
		}
		if err := r.Folder.Create(ctx, &folder); err != nil {
			return err
		}

		f := &models.BaseFile{
			Path:           "/marker_sync_test/galleries/g.zip",
			Basename:       "g.zip",
			ParentFolderID: folder.ID,
			Fingerprints: []models.Fingerprint{
				{Type: models.FingerprintTypeMD5, Fingerprint: md5},
			},
		}
		if err := r.File.Create(ctx, f); err != nil {
			return err
		}

		g := models.NewGallery()
		g.Title = "attach me"
		if err := r.Gallery.Create(ctx, &models.CreateGalleryInput{
			Gallery: &g,
			FileIDs: []models.FileID{f.ID},
		}); err != nil {
			return err
		}
		galleryID = g.ID
		return nil
	}); err != nil {
		t.Fatalf("setup: %v", err)
	}

	task := &markerSyncTask{scene: &models.Scene{ID: sceneID}, syncGalleries: true}
	p := &fakeGalleryProvider{galleries: []markersync.GalleryRef{{
		MD5s: []string{md5},
		URLs: []string{"https://gallery.example/g1"},
	}}}

	task.applyGalleries(ctx, r, "fake", p, markersync.SceneIdentity{})

	if err := txn.WithReadTxn(ctx, r.TxnManager, func(ctx context.Context) error {
		g, err := r.Gallery.Find(ctx, galleryID)
		if err != nil {
			return err
		}
		if err := g.LoadSceneIDs(ctx, r.Gallery); err != nil {
			return err
		}
		sceneIDs := g.SceneIDs.List()
		if len(sceneIDs) != 1 || sceneIDs[0] != sceneID {
			t.Fatalf("gallery scene ids = %v, want [%d]", sceneIDs, sceneID)
		}
		if err := g.LoadURLs(ctx, r.Gallery); err != nil {
			return err
		}
		urls := g.URLs.List()
		if len(urls) != 1 || urls[0] != "https://gallery.example/g1" {
			t.Fatalf("gallery urls = %v, want [https://gallery.example/g1]", urls)
		}
		return nil
	}); err != nil {
		t.Fatalf("verifying gallery: %v", err)
	}
}

// TestMarkerSyncScenesExcludesSkipSyncTag proves the fetch-path scene selection
// excludes scenes bearing the skip-sync tag, mirroring the submit path.
func TestMarkerSyncScenesExcludesSkipSyncTag(t *testing.T) {
	r := newTestRepository(t)
	ctx := context.Background()
	s := &Manager{Repository: r}

	var keepID, skipID int
	if err := txn.WithTxn(ctx, r.TxnManager, func(ctx context.Context) error {
		tag := models.NewTag()
		tag.Name = ttSkipSyncTagName
		if err := r.Tag.Create(ctx, &models.CreateTagInput{Tag: &tag}); err != nil {
			return err
		}

		keep := models.NewScene()
		keep.Title = "keep me"
		if err := r.Scene.Create(ctx, &keep, nil); err != nil {
			return err
		}
		keepID = keep.ID

		skip := models.NewScene()
		skip.Title = "skip me"
		if err := r.Scene.Create(ctx, &skip, nil); err != nil {
			return err
		}
		skipID = skip.ID

		_, err := r.Scene.UpdatePartial(ctx, skipID, models.ScenePartial{
			TagIDs: &models.UpdateIDs{IDs: []int{tag.ID}, Mode: models.RelationshipUpdateModeAdd},
		})
		return err
	}); err != nil {
		t.Fatalf("setup: %v", err)
	}

	all := MarkerSyncSelectModeAll
	scenes, err := s.markerSyncScenes(ctx, MarkerSyncInput{Mode: &all})
	if err != nil {
		t.Fatalf("markerSyncScenes: %v", err)
	}

	ids := map[int]bool{}
	for _, sc := range scenes {
		ids[sc.ID] = true
	}
	if !ids[keepID] {
		t.Errorf("keep scene %d missing from result (should be synced)", keepID)
	}
	if ids[skipID] {
		t.Errorf("skip-sync-tagged scene %d present in result, want excluded", skipID)
	}
}

// TestMarkerSyncIntegration exercises the real repository-backed MarkerWriter
// and TagResolver against markersync.Apply end-to-end on a live sqlite database.
func TestMarkerSyncIntegration(t *testing.T) {
	r := newTestRepository(t)
	ctx := context.Background()

	// create a scene to attach markers to
	var sceneID int
	if err := txn.WithTxn(ctx, r.TxnManager, func(ctx context.Context) error {
		sc := models.NewScene()
		sc.Title = "marker sync test scene"
		if err := r.Scene.Create(ctx, &sc, nil); err != nil {
			return err
		}
		sceneID = sc.ID
		return nil
	}); err != nil {
		t.Fatalf("creating scene: %v", err)
	}

	candidates := []markersync.MarkerCandidate{
		{Title: "BJ", PrimaryTag: "Blowjob", Seconds: 10},
		{
			Title:      "CG",
			PrimaryTag: "Cowgirl",
			Seconds:    120,
			EndSeconds: ptrFloat(150),
			ExtraTags:  []string{"Position"},
		},
	}

	opts := markersync.ApplyOptions{
		Tolerance: markersync.DefaultTolerance,
		Mode:      markersync.ModeSkip,
		TagAware:  true,
	}

	// helper to run a single apply pass in its own transaction
	apply := func(cands []markersync.MarkerCandidate, o markersync.ApplyOptions) markersync.ApplyResult {
		t.Helper()
		var res markersync.ApplyResult
		if err := txn.WithTxn(ctx, r.TxnManager, func(ctx context.Context) error {
			w := &markerWriter{r: r}
			tags := markersync.NewCachingTagResolver(&tagResolver{r: r.Tag})
			var err error
			res, err = markersync.Apply(ctx, w, tags, sceneID, cands, o)
			return err
		}); err != nil {
			t.Fatalf("apply: %v", err)
		}
		return res
	}

	// resolve a tag id by name in a read txn
	tagIDByName := func(name string) int {
		t.Helper()
		var id int
		if err := txn.WithReadTxn(ctx, r.TxnManager, func(ctx context.Context) error {
			tg, err := r.Tag.FindByName(ctx, name, true)
			if err != nil {
				return err
			}
			if tg == nil {
				return nil
			}
			id = tg.ID
			return nil
		}); err != nil {
			t.Fatalf("finding tag %q: %v", name, err)
		}
		return id
	}

	markersForScene := func() []*models.SceneMarker {
		t.Helper()
		var ms []*models.SceneMarker
		if err := txn.WithReadTxn(ctx, r.TxnManager, func(ctx context.Context) error {
			var err error
			ms, err = r.SceneMarker.FindBySceneID(ctx, sceneID)
			return err
		}); err != nil {
			t.Fatalf("loading markers: %v", err)
		}
		return ms
	}

	// --- Pass 1: initial create ---
	res := apply(candidates, opts)
	if res.Created != 2 || res.Skipped != 0 || res.Overwritten != 0 {
		t.Fatalf("pass 1: got created=%d skipped=%d overwritten=%d, want 2/0/0", res.Created, res.Skipped, res.Overwritten)
	}

	// primary tags were get-or-created by name
	blowjobID := tagIDByName("Blowjob")
	cowgirlID := tagIDByName("Cowgirl")
	if blowjobID == 0 || cowgirlID == 0 {
		t.Fatalf("primary tags not created: blowjob=%d cowgirl=%d", blowjobID, cowgirlID)
	}

	ms := markersForScene()
	if len(ms) != 2 {
		t.Fatalf("pass 1: got %d markers, want 2", len(ms))
	}

	byPrimary := map[int]*models.SceneMarker{}
	for _, m := range ms {
		byPrimary[m.PrimaryTagID] = m
	}

	bj := byPrimary[blowjobID]
	if bj == nil || bj.Seconds != 10 {
		t.Fatalf("blowjob marker wrong: %+v", bj)
	}
	cg := byPrimary[cowgirlID]
	if cg == nil || cg.Seconds != 120 {
		t.Fatalf("cowgirl marker wrong: %+v", cg)
	}
	if cg.EndSeconds == nil || *cg.EndSeconds != 150 {
		t.Fatalf("cowgirl end seconds wrong: %+v", cg.EndSeconds)
	}

	// extra tag "Position" associated with the cowgirl marker (and not the
	// primary tag)
	if err := txn.WithReadTxn(ctx, r.TxnManager, func(ctx context.Context) error {
		extra, err := r.Tag.FindBySceneMarkerID(ctx, cg.ID)
		if err != nil {
			return err
		}
		if len(extra) != 1 || extra[0].Name != "Position" {
			t.Fatalf("cowgirl extra tags: got %+v, want [Position]", extra)
		}
		return nil
	}); err != nil {
		t.Fatalf("loading extra tags: %v", err)
	}

	// --- Pass 2: idempotent re-run (skip mode) ---
	res = apply(candidates, opts)
	if res.Created != 0 || res.Skipped != 2 || res.Overwritten != 0 {
		t.Fatalf("pass 2: got created=%d skipped=%d overwritten=%d, want 0/2/0", res.Created, res.Skipped, res.Overwritten)
	}
	if got := len(markersForScene()); got != 2 {
		t.Fatalf("pass 2: got %d markers, want 2 (no duplicates)", got)
	}

	// capture the current marker ids to prove overwrite replaces them
	oldIDs := map[int]bool{}
	for _, m := range markersForScene() {
		oldIDs[m.ID] = true
	}

	// --- Pass 3: overwrite mode replaces the matched markers ---
	overwriteOpts := opts
	overwriteOpts.Mode = markersync.ModeOverwrite
	res = apply(candidates, overwriteOpts)
	if res.Overwritten != 2 || res.Created != 0 || res.Skipped != 0 {
		t.Fatalf("pass 3: got created=%d skipped=%d overwritten=%d, want 0/0/2", res.Created, res.Skipped, res.Overwritten)
	}

	after := markersForScene()
	if len(after) != 2 {
		t.Fatalf("pass 3: got %d markers, want 2 (replaced, not appended)", len(after))
	}
	for _, m := range after {
		if oldIDs[m.ID] {
			t.Fatalf("pass 3: marker id %d was not replaced", m.ID)
		}
	}
}
