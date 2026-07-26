// autotag-bench runs the file-based auto-tag flow against a synthetic
// sqlite database populated with configurable numbers of performers,
// studios, tags, and files. It reports wall-clock time and memory
// footprint so two builds (e.g. baseline vs. perf changes) can be
// compared side-by-side.
//
// Usage:
//
//	go run ./cmd/autotag-bench --preset=tiny --out=bench.md --label=new
//
// Presets: tiny | small | medium | large (see synth.go).
package main

import (
	"context"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"runtime"
	"runtime/debug"
	"syscall"
	"time"
)

func main() {
	var (
		presetName string
		outPath    string
		label      string
		seed       int64
	)
	flag.StringVar(&presetName, "preset", "tiny", "tiny|small|medium|large")
	flag.StringVar(&outPath, "out", "", "write markdown result here (stdout if empty)")
	flag.StringVar(&label, "label", "run", "label for this run (e.g. baseline, new)")
	flag.Int64Var(&seed, "seed", 42, "rng seed for deterministic data generation")
	flag.Parse()

	p, ok := presets[presetName]
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown preset %q (valid: tiny, small, medium, large)\n", presetName)
		os.Exit(2)
	}

	ctx := context.Background()
	rng := rand.New(rand.NewSource(seed))

	fmt.Fprintf(os.Stderr, "[%s] setting up %s: %d performers, %d studios, %d tags, %d files\n",
		label, p.name, p.performers, p.studios, p.tags, p.files)

	s, err := setup(ctx, p, rng)
	if err != nil {
		fmt.Fprintf(os.Stderr, "setup failed: %v\n", err)
		os.Exit(1)
	}
	defer s.cleanup()

	fmt.Fprintf(os.Stderr, "[%s] setup took %s; db at %s\n", label, s.setupTook, s.dbPath)

	// Force a GC before measurement so the memory snapshot reflects what
	// the autotag flow allocates, not leftover setup garbage.
	runtime.GC()
	debug.FreeOSMemory()
	var memBefore runtime.MemStats
	runtime.ReadMemStats(&memBefore)
	rssBefore := maxRSSBytes()

	fmt.Fprintf(os.Stderr, "[%s] running auto-tag...\n", label)
	begin := time.Now()
	runAutoTag(ctx, s.repo)
	elapsed := time.Since(begin)

	var memAfter runtime.MemStats
	runtime.ReadMemStats(&memAfter)
	rssAfter := maxRSSBytes()

	r := result{
		label:        label,
		preset:       p,
		setupTook:    s.setupTook,
		runTook:      elapsed,
		totalAlloc:   memAfter.TotalAlloc - memBefore.TotalAlloc,
		heapInUse:    memAfter.HeapInuse,
		numGC:        memAfter.NumGC - memBefore.NumGC,
		peakRSSBytes: rssAfter - rssBefore,
		peakRSSAbs:   rssAfter,
	}

	fmt.Fprintf(os.Stderr, "[%s] done: runtime=%s heap_in_use=%s peak_rss_delta=%s\n",
		label, r.runTook, humanBytes(r.heapInUse), humanBytes(r.peakRSSBytes))

	md := formatResult(r)
	if outPath == "" {
		fmt.Print(md)
	} else {
		if err := os.WriteFile(outPath, []byte(md), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "writing result: %v\n", err)
			os.Exit(1)
		}
	}
}

// result captures the numbers we want to compare across runs.
type result struct {
	label        string
	preset       preset
	setupTook    time.Duration
	runTook      time.Duration
	totalAlloc   uint64
	heapInUse    uint64
	numGC        uint32
	peakRSSBytes uint64 // rusage Maxrss delta from pre-run to post-run
	peakRSSAbs   uint64 // absolute Maxrss at end (process-lifetime peak)
}

// maxRSSBytes returns the process's peak resident-set size in bytes, using
// getrusage. On Linux ru_maxrss is in KB; on macOS it is already in bytes.
func maxRSSBytes() uint64 {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0
	}
	if runtime.GOOS == "linux" {
		return uint64(ru.Maxrss) * 1024
	}
	// darwin, freebsd, etc. report bytes directly
	return uint64(ru.Maxrss)
}

func humanBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%dB", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

// formatResult emits a single-row markdown table that the runner script
// later concatenates per preset.
func formatResult(r result) string {
	return fmt.Sprintf(`| %s | %s | %s | %s | %s | %d |
`,
		r.label,
		r.runTook.Round(time.Millisecond),
		humanBytes(r.heapInUse),
		humanBytes(r.peakRSSAbs),
		humanBytes(r.totalAlloc),
		r.numGC,
	)
}
