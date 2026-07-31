package python

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

type releaseAsset struct {
	Archive string
	SHA256  string
	Libc    string
}

var managedUVReleases = map[string][]releaseAsset{
	"windows/amd64": {{Archive: "uv-x86_64-pc-windows-msvc.zip", SHA256: "c253ce868ad48d29327b661452ce184c9e333e6d6f5bc8d6fcfbf4dd52b83442"}},
	"darwin/amd64":  {{Archive: "uv-x86_64-apple-darwin.tar.gz", SHA256: "f1b919f740bd6be1d014ff58c4271b0779a32198adfb19ad9c5d1c4d9b2b4301"}},
	"darwin/arm64":  {{Archive: "uv-aarch64-apple-darwin.tar.gz", SHA256: "d75e3d2bfc203d17388edaabd3aa37958edbcbfc36219e3ee0d31bb080b4baa2"}},
	"linux/amd64": {
		{Archive: "uv-x86_64-unknown-linux-gnu.tar.gz", SHA256: "aa9fca823c03289fb6e3460b3dc864f3ea895cafaf9b99247701a67b17d1b018", Libc: "gnu"},
		{Archive: "uv-x86_64-unknown-linux-musl.tar.gz", SHA256: "3e95b84d8a8b3390c91584d4fd0c4d326e951da2b8fb15a76719368af2795424", Libc: "musl"},
	},
	"linux/arm64": {
		{Archive: "uv-aarch64-unknown-linux-gnu.tar.gz", SHA256: "9ed88a9a42de3102f9704d021ab186fdf8a69a7ad9a1d3f3486ac6b1e55d6141", Libc: "gnu"},
		{Archive: "uv-aarch64-unknown-linux-musl.tar.gz", SHA256: "e256e5cf23f8c2e7d4d83f029acd662cbe7275ff24268ce1d6d2ba9016855268", Libc: "musl"},
	},
	"linux/arm": {
		{Archive: "uv-armv7-unknown-linux-gnueabihf.tar.gz", SHA256: "5446f812e7f5512198adaf60d2c66672a41d9676e86686819222baef021bce5b", Libc: "gnu"},
		{Archive: "uv-armv7-unknown-linux-musleabihf.tar.gz", SHA256: "9e0f6904f62997e34ee8795d615ba2974f31b0461674572d05859ba0b9402290", Libc: "musl"},
	},
}

func currentReleaseAssets(goos, goarch string, musl bool) ([]releaseAsset, error) {
	assets := managedUVReleases[goos+"/"+goarch]
	if len(assets) == 0 {
		return nil, fmt.Errorf("uv %s is not available for this platform", ManagedUVVersion)
	}
	if len(assets) == 1 {
		return assets, nil
	}
	preferred := "gnu"
	if musl {
		preferred = "musl"
	}
	ordered := make([]releaseAsset, 0, len(assets))
	for _, asset := range assets {
		if asset.Libc == preferred {
			ordered = append(ordered, asset)
		}
	}
	for _, asset := range assets {
		if asset.Libc != preferred {
			ordered = append(ordered, asset)
		}
	}
	return ordered, nil
}

func hostPlatform() (string, string, bool) {
	return runtime.GOOS, runtime.GOARCH, hostUsesMusl()
}

func hostUsesMusl() bool {
	if _, err := os.Stat("/etc/alpine-release"); err == nil {
		return true
	}
	for _, pattern := range []string{"/lib/ld-musl-*.so.1", "/usr/lib/ld-musl-*.so.1"} {
		if matches, _ := filepath.Glob(pattern); len(matches) != 0 {
			return true
		}
	}
	return false
}
