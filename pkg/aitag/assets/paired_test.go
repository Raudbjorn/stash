package assets

import (
	"context"
	"strings"
	"testing"
)

// Every pair must be installable, or the provider offers a model it cannot get.
func TestVisionPairsAreDownloadable(t *testing.T) {
	pairs := VisionPairs()
	if len(pairs) == 0 {
		t.Fatal("no vision pairs are offered")
	}

	for _, p := range pairs {
		t.Run(p.Name, func(t *testing.T) {
			if !p.Downloadable() {
				t.Fatal("the pair is not downloadable; FetchPair requires a checksum on BOTH halves")
			}
			for _, a := range p.Assets() {
				if len(a.SHA256) != 64 {
					t.Errorf("%s: checksum is %d characters, want 64 hex", a.Name, len(a.SHA256))
				}
				if a.Size <= 0 {
					t.Errorf("%s declares no size", a.Name)
				}
				if !strings.HasPrefix(a.URL, "https://") {
					t.Errorf("%s: %q is not HTTPS", a.Name, a.URL)
				}
				if a.License == "" || a.LicenseURL == "" {
					t.Errorf("%s does not carry complete licence metadata", a.Name)
				}
				if a.Description == "" {
					t.Errorf("%s has no description", a.Name)
				}
			}
			if p.ContextTokens <= 0 {
				t.Error("the pair declares no context window; it must hold the encoded image AND the prompt")
			}
		})
	}
}

// Licence is checked against the UPSTREAM weights, not the GGUF repository's own
// tag - they disagree, and the disagreement is the trap. ggml-org's
// Qwen2.5-VL-3B GGUF is tagged apache-2.0 while the weights it was converted
// from are under the Qwen RESEARCH licence. Downloadable is not free.
func TestVisionPairsAreFreelyLicensed(t *testing.T) {
	allowed := map[string]bool{LicenseApache2: true, LicenseMIT: true}

	for _, p := range VisionPairs() {
		if !allowed[p.License] {
			t.Errorf("%s is licensed %q; only Apache-2.0 and MIT are offered", p.Name, p.License)
		}
		if p.LicenseURL == "" {
			t.Errorf("%s names no licence source to check against", p.Name)
		}
		for _, a := range p.Assets() {
			if !allowed[a.License] {
				t.Errorf("%s/%s is licensed %q", p.Name, a.Name, a.License)
			}
		}
	}
}

// The specific artifacts excluded on licence grounds, named so that adding one
// back is a decision rather than an oversight.
func TestRestrictedModelsAreNotOffered(t *testing.T) {
	banned := []struct{ fragment, why string }{
		{"Qwen2.5-VL-3B", "the upstream 3B weights are under the Qwen Research licence, unlike the 2B and 7B"},
		{"gemma-3", "the Gemma Terms carry a prohibited-use policy"},
	}

	for _, p := range VisionPairs() {
		for _, a := range p.Assets() {
			for _, b := range banned {
				if strings.Contains(strings.ToLower(a.URL), strings.ToLower(b.fragment)) {
					t.Errorf("%s offers %s: %s", p.Name, b.fragment, b.why)
				}
			}
		}
	}
}

func TestDefaultVisionPairResolves(t *testing.T) {
	pair, ok := FindPair(DefaultVisionPair)
	if !ok {
		t.Fatalf("DefaultVisionPair %q is not in VisionPairs()", DefaultVisionPair)
	}
	if !pair.Downloadable() {
		t.Error("the default pair cannot be downloaded")
	}

	// The default must fit the card this was designed against. Both halves are
	// resident at once, so the sum is what matters, not the larger of the two.
	var total int64
	for _, a := range pair.Assets() {
		total += a.Size
	}
	if limit := int64(3) << 30; total > limit {
		t.Errorf("the default pair is %d bytes; over %d it will not fit a 4 GB card "+
			"alongside its context, and a model that swaps is worse than a smaller one",
			total, limit)
	}
}

// A pair with one verifiable half must be refused BEFORE anything downloads:
// the half-installed combination reports as not-installed and fails at load.
func TestFetchPairRefusesAHalfPinnedPair(t *testing.T) {
	pair := Pair{
		Name:      "broken",
		Primary:   Asset{Name: "m.gguf", URL: "https://example.com/m.gguf", SHA256: digest(nil)},
		Companion: Asset{Name: "p.gguf", URL: "https://example.com/p.gguf"},
	}

	d := &Downloader{Dir: t.TempDir()}
	_, _, err := d.FetchPair(context.Background(), pair, nil)
	if err == nil {
		t.Fatal("a pair with an unverifiable projector was accepted")
	}
	if !strings.Contains(err.Error(), "checksum") {
		t.Errorf("the error does not explain why: %v", err)
	}
}
