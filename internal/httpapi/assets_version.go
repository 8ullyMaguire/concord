package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"html/template"
	"io/fs"
	"net/http"
)

// assetHashes maps a static asset path to a short content hash, computed once
// at startup from the embedded filesystem.
//
// Why this exists: the assets are served from an embed.FS, so their URL never
// changes and the browser is entitled to keep the copy it has. A deploy that
// changes project.js therefore does not reach a browser that already loaded the
// old one. I lost several rounds debugging a form that was working perfectly,
// because the script the browser held was the one from before a fix.
//
// The fix is content-addressed URLs: /assets/js/project.js?v=<hash> is a
// different URL when the content differs, so the browser's cache is bypassed
// exactly when it should be and reused when it should be.
var assetHashes map[string]string

// loadAssetHashes walks the embedded assets and records a short hash per file.
func loadAssetHashes(fsys fs.FS) {
	assetHashes = make(map[string]string)
	_ = fs.WalkDir(fsys, "assets", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		b, err := fs.ReadFile(fsys, p)
		if err != nil {
			return nil
		}
		sum := sha256.Sum256(b)
		assetHashes["/"+p] = hex.EncodeToString(sum[:])[:12]
		return nil
	})
}

// assetVersion returns the version query for a path, or "" if unknown.
func assetVersion(path string) string {
	if assetHashes == nil {
		return ""
	}
	return assetHashes[path]
}

// staticHandler serves embedded assets with cache headers that match the
// content-addressed URLs.
//
// A hashed URL may be cached forever, because its content can never change. The
// bare path must be revalidated. Serving both with a long max-age is what
// created the staleness in the first place.
func staticHandler() http.Handler {
	fileServer := http.FileServer(http.FS(staticFS))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("v") == assetVersion(r.URL.Path) && assetVersion(r.URL.Path) != "" {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		fileServer.ServeHTTP(w, r)
	})
}

// assetURL adds the version query to a static asset path, so markup cannot
// forget it.
func assetURL(path string) template.URL {
	return template.URL(path + "?v=" + assetVersion(path))
}

// assetFunc is what templates call: {{ asset "/assets/js/project.js" }}
func assetFunc() func(string) template.URL { return assetURL }
