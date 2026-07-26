package manager

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/stashapp/stash/pkg/file/video"
	"github.com/stashapp/stash/pkg/fsutil"
	"github.com/stashapp/stash/pkg/logger"
	"github.com/syncthing/notify"
)

// How long to wait after the last write event to a path before scanning it, to
// avoid scanning a file that is still being copied in.
const defaultWriteDebounce = 30 * time.Second

// findAssociatedVideo maps a sidecar file event (funscript/caption) to the
// sibling video it belongs to, so the video is (re)scanned and the association
// is picked up. Returns "" if no associated video is found.
func (s *Manager) findAssociatedVideo(eventPath string) string {
	dir := filepath.Dir(eventPath)

	// funscript: same basename as the video, different extension.
	if fsutil.MatchExtension(eventPath, []string{"funscript"}) {
		stem := strings.TrimSuffix(filepath.Base(eventPath), filepath.Ext(eventPath))
		for _, ve := range s.Config.GetVideoExtensions() {
			cand := filepath.Join(dir, stem+"."+ve)
			if _, err := os.Stat(cand); err == nil {
				return cand
			}
		}
		return ""
	}

	// caption (.srt/.vtt): find the sibling video it matches.
	if fsutil.MatchExtension(eventPath, video.CaptionExts) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return ""
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			cand := filepath.Join(dir, e.Name())
			if useAsVideo(cand) && video.MatchesCaption(cand, eventPath) {
				return cand
			}
		}
	}

	return ""
}

// shouldScheduleScan returns the path to scan for a raw event path, or "" if the
// event should be ignored. Video/image/zip files are scanned directly; sidecar
// files are mapped to their associated video.
func (s *Manager) shouldScheduleScan(rawPath string) string {
	if useAsVideo(rawPath) || useAsImage(rawPath) || isZip(rawPath) {
		return rawPath
	}
	return s.findAssociatedVideo(rawPath)
}

// watcherScan enqueues a scan job for the changed path, reusing the same scan
// entry point as manual and scheduled scans.
func (s *Manager) watcherScan(p string, fi os.FileInfo) {
	if !fi.IsDir() {
		if mapped := s.shouldScheduleScan(p); mapped != "" {
			p = mapped
		} else {
			return
		}
	}

	// Use a background context so a watcher restart doesn't cancel an in-flight
	// scan; the job manager owns the job's lifecycle.
	if _, err := s.Scan(context.Background(), ScanMetadataInput{Paths: []string{p}}); err != nil {
		logger.Errorf("watcher: scan of %q failed: %v", p, err)
	}
}

// StopFileWatcher stops the filesystem watcher if it is running.
func (s *Manager) StopFileWatcher() {
	s.fileWatcherMu.Lock()
	defer s.fileWatcherMu.Unlock()
	if s.fileWatcherCancel != nil {
		s.fileWatcherCancel()
		s.fileWatcherCancel = nil
	}
}

// RefreshFileWatcher (re)starts the filesystem watcher for the configured stash
// library paths. When auto_scan_watch is disabled it simply stops any running
// watcher. Safe to call repeatedly (e.g. after a config change).
func (s *Manager) RefreshFileWatcher() {
	s.fileWatcherMu.Lock()
	if s.fileWatcherCancel != nil {
		s.fileWatcherCancel()
		s.fileWatcherCancel = nil
	}

	if !s.Config.GetAutoScanWatch() {
		s.fileWatcherMu.Unlock()
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	s.fileWatcherCancel = cancel
	s.fileWatcherMu.Unlock()

	go s.runFileWatcher(ctx)
}

func (s *Manager) runFileWatcher(ctx context.Context) {
	events := make(chan notify.EventInfo, 1024)

	go func() {
		<-ctx.Done()
		notify.Stop(events)
	}()

	for _, st := range s.Config.GetStashPaths() {
		if st == nil || st.Path == "" {
			continue
		}

		// trailing "..." requests a recursive watch.
		path := filepath.Clean(st.Path) + "..."
		if err := notify.Watch(path, events, notifyEvents()...); err != nil {
			logger.Errorf("watcher: failed to watch %s: %v", st.Path, err)
			return
		}
		logger.Infof("watcher: watching %s for changes", st.Path)
	}

	var mu sync.Mutex
	timers := make(map[string]*time.Timer)

	for {
		select {
		case <-ctx.Done():
			return

		case ev, ok := <-events:
			if !ok {
				return
			}

			rawPath := ev.Path()

			// Debounce bursts of write events per path.
			mu.Lock()
			if t, ok := timers[rawPath]; ok {
				t.Stop()
			}
			timers[rawPath] = time.AfterFunc(defaultWriteDebounce, func() {
				mu.Lock()
				delete(timers, rawPath)
				mu.Unlock()

				fi, err := os.Stat(rawPath)
				if err != nil {
					// file removed/renamed before the debounce elapsed
					return
				}
				if notifyShouldScanEvent(fi, ev) {
					s.watcherScan(rawPath, fi)
				}
			})
			mu.Unlock()
		}
	}
}
