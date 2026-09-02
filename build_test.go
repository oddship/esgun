package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixtureProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := copyTree(filepath.Join("testdata", "profile-project"), dir); err != nil {
		t.Fatal(err)
	}
	return dir
}

func mustWrite(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertFile(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(contents)
}

func TestBuildProfilesAndCopies(t *testing.T) {
	dir := fixtureProject(t)
	mustWrite(t, filepath.Join(dir, "dist", "stale.txt"), "stale")
	if err := build(dir, ""); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"library.esm.js", "library.min.js", "base.css", "theme.css", "assets/readme.txt"} {
		assertFile(t, filepath.Join(dir, "dist", filepath.FromSlash(name)))
	}
	if _, err := os.Stat(filepath.Join(dir, "dist", "stale.txt")); !os.IsNotExist(err) {
		t.Fatalf("stale output was not removed: %v", err)
	}
	if got := assertFile(t, filepath.Join(dir, "dist", "library.esm.js")); !strings.Contains(got, "export") {
		t.Fatalf("readable ESM output did not retain export: %s", got)
	}
	if got := assertFile(t, filepath.Join(dir, "dist", "library.esm.js")); !strings.Contains(got, "#value") {
		t.Fatalf("esnext target unexpectedly lowered private fields: %s", got)
	}
	if got := assertFile(t, filepath.Join(dir, "dist", "library.min.js")); strings.Contains(got, "// src/") {
		t.Fatalf("minified IIFE retained readable source comments: %s", got)
	}
}

func TestSelectedProfileCleansRootAndRunsGlobalCopies(t *testing.T) {
	dir := fixtureProject(t)
	if err := build(dir, "esm-readable"); err != nil {
		t.Fatal(err)
	}
	assertFile(t, filepath.Join(dir, "dist", "library.esm.js"))
	assertFile(t, filepath.Join(dir, "dist", "assets", "readme.txt"))
	if _, err := os.Stat(filepath.Join(dir, "dist", "library.min.js")); !os.IsNotExist(err) {
		t.Fatalf("unselected profile output exists: %v", err)
	}
}

func TestLegacyManifestRemainsSupported(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "src", "main.js"), "export const value = 1;\n")
	mustWrite(t, filepath.Join(dir, "static", "note.txt"), "legacy")
	mustWrite(t, filepath.Join(dir, "esgun.config.json"), `{
  "root":"dist", "outdir":"dist/js", "entry":["src/main.js"],
  "copies":[{"from":"static", "to":"dist/static"}], "futureLegacyField":true
}`)
	if err := build(dir, ""); err != nil {
		t.Fatal(err)
	}
	assertFile(t, filepath.Join(dir, "dist", "js", "main.js"))
	assertFile(t, filepath.Join(dir, "dist", "static", "note.txt"))
}

func TestProfileValidationPreservesExistingOutput(t *testing.T) {
	tests := map[string]string{
		"unknown field":  `{"root":"dist","entrySets":{"app":[{"input":"src/main.js","output":"app"}]},"profiles":{"x":{"entrySets":["app"],"outdir":"dist","format":"esm","unknown":true}}}`,
		"iife splitting": `{"root":"dist","entrySets":{"app":[{"input":"src/main.js","output":"app"}]},"profiles":{"x":{"entrySets":["app"],"outdir":"dist","format":"iife","splitting":true}}}`,
		"unsafe suffix":  `{"root":"dist","entrySets":{"app":[{"input":"src/main.js","output":"app"}]},"profiles":{"x":{"entrySets":["app"],"outdir":"dist","format":"esm","suffix":"."}}}`,
		"invalid target": `{"root":"dist","target":"latest-ish","entrySets":{"app":[{"input":"src/main.js","output":"app"}]},"profiles":{"x":{"entrySets":["app"],"outdir":"dist","format":"esm"}}}`,
		"missing set":    `{"root":"dist","entrySets":{"app":[{"input":"src/main.js","output":"app"}]},"profiles":{"x":{"entrySets":["missing"],"outdir":"dist","format":"esm"}}}`,
		"mixed mode":     `{"root":"dist","entry":["src/main.js"],"entrySets":{"app":[{"input":"src/main.js","output":"app"}]},"profiles":{"x":{"entrySets":["app"],"outdir":"dist","format":"esm"}}}`,
	}
	for name, config := range tests {
		t.Run(name, func(t *testing.T) {
			dir := fixtureProject(t)
			mustWrite(t, filepath.Join(dir, "dist", "keep.txt"), "last good")
			mustWrite(t, filepath.Join(dir, "esgun.config.json"), config)
			if err := build(dir, ""); err == nil {
				t.Fatal("expected validation error")
			}
			if got := assertFile(t, filepath.Join(dir, "dist", "keep.txt")); got != "last good" {
				t.Fatalf("existing output changed: %q", got)
			}
		})
	}
}

func TestBuildFailurePreservesExistingOutput(t *testing.T) {
	dir := fixtureProject(t)
	mustWrite(t, filepath.Join(dir, "dist", "keep.txt"), "last good")
	mustWrite(t, filepath.Join(dir, "src", "main.js"), "export const = ;")
	if err := build(dir, ""); err == nil {
		t.Fatal("expected esbuild failure")
	}
	assertFile(t, filepath.Join(dir, "dist", "keep.txt"))
}

func TestProfileAndCopyCollisionsPreserveOutput(t *testing.T) {
	tests := map[string]string{
		"profiles":         `{"root":"dist","entrySets":{"app":[{"input":"src/main.js","output":"same"}]},"profiles":{"a":{"entrySets":["app"],"outdir":"dist","format":"esm"},"b":{"entrySets":["app"],"outdir":"dist","format":"iife"}}}`,
		"copy":             `{"root":"dist","entrySets":{"app":[{"input":"src/main.js","output":"library"}]},"profiles":{"esm":{"entrySets":["app"],"outdir":"dist","format":"esm","suffix":".esm"}},"copies":[{"from":"assets/readme.txt","to":"dist/library.esm.js"}]}`,
		"css sidecar copy": `{"root":"dist","entrySets":{"app":[{"input":"src/main.js","output":"library"}]},"profiles":{"esm":{"entrySets":["app"],"outdir":"dist","format":"esm"}},"copies":[{"from":"assets/readme.txt","to":"dist/library.css"}]}`,
	}
	for name, config := range tests {
		t.Run(name, func(t *testing.T) {
			dir := fixtureProject(t)
			if name == "css sidecar copy" {
				mustWrite(t, filepath.Join(dir, "src", "main.js"), "import './styles/base.css'; export const ok = true;\n")
			}
			mustWrite(t, filepath.Join(dir, "dist", "keep.txt"), "last good")
			mustWrite(t, filepath.Join(dir, "esgun.config.json"), config)
			if err := build(dir, ""); err == nil || !strings.Contains(err.Error(), "collision") {
				t.Fatalf("expected collision, got %v", err)
			}
			assertFile(t, filepath.Join(dir, "dist", "keep.txt"))
		})
	}
}

func TestWatchRejectsDefaultDirectoryOutputOverlapBeforeBuild(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "src", "main.js"), "export default 1;\n")
	mustWrite(t, filepath.Join(dir, "src", "dist", "keep.txt"), "last good")
	mustWrite(t, filepath.Join(dir, "esgun.config.json"), `{"root":"src/dist","outdir":"src/dist/js","entry":["src/main.js"]}`)
	if err := watch(dir, ""); err == nil || !strings.Contains(err.Error(), "overlaps output root") {
		t.Fatalf("expected watch/root overlap error, got %v", err)
	}
	assertFile(t, filepath.Join(dir, "src", "dist", "keep.txt"))
}

func TestWatchValidatesEffectiveDirectoriesBeforeBuild(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "app", "main.js"), "export default 1;\n")
	mustWrite(t, filepath.Join(dir, "dist", "keep.txt"), "last good")
	mustWrite(t, filepath.Join(dir, "esgun.config.json"), `{"root":"dist","outdir":"dist/js","entry":["app/main.js"]}`)
	if err := watch(dir, ""); err == nil || !strings.Contains(err.Error(), "watch path") {
		t.Fatalf("expected missing default watch directory error, got %v", err)
	}
	assertFile(t, filepath.Join(dir, "dist", "keep.txt"))
}

func TestBuildIgnoresMissingWatchOnlyDirectory(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "src", "main.js"), "export default 1;\n")
	mustWrite(t, filepath.Join(dir, "esgun.config.json"), `{"root":"dist","outdir":"dist/js","entry":["src/main.js"],"watch":["dev-only"]}`)
	if err := build(dir, ""); err != nil {
		t.Fatalf("ordinary build should ignore a missing watch-only directory: %v", err)
	}
	assertFile(t, filepath.Join(dir, "dist", "js", "main.js"))
}

func TestUnsafeAndOverlappingPathsAreRejected(t *testing.T) {
	t.Run("root traversal", func(t *testing.T) {
		dir := fixtureProject(t)
		mustWrite(t, filepath.Join(dir, "esgun.config.json"), `{"root":"..","entry":["src/main.js"]}`)
		if err := build(dir, ""); err == nil {
			t.Fatal("expected unsafe root error")
		}
	})
	t.Run("root symlink", func(t *testing.T) {
		dir := fixtureProject(t)
		if err := os.Symlink("src", filepath.Join(dir, "linked-dist")); err != nil {
			t.Fatal(err)
		}
		mustWrite(t, filepath.Join(dir, "esgun.config.json"), `{"root":"linked-dist","outdir":"linked-dist/js","entry":["src/main.js"]}`)
		if err := build(dir, ""); err == nil {
			t.Fatal("expected root symlink error")
		}
		assertFile(t, filepath.Join(dir, "src", "main.js"))
	})
	t.Run("recursive copy", func(t *testing.T) {
		dir := fixtureProject(t)
		mustWrite(t, filepath.Join(dir, "esgun.config.json"), `{"root":"dist","entry":["src/main.js"],"copies":[{"from":".","to":"dist/project"}]}`)
		if err := build(dir, ""); err == nil {
			t.Fatal("expected overlapping copy error")
		}
	})
}

func TestUnknownAndSelectedProfiles(t *testing.T) {
	dir := fixtureProject(t)
	if err := build(dir, "missing"); err == nil || !strings.Contains(err.Error(), "unknown profile") {
		t.Fatalf("expected unknown profile error, got %v", err)
	}
	dir = t.TempDir()
	mustWrite(t, filepath.Join(dir, "src", "main.js"), "export default 1")
	mustWrite(t, filepath.Join(dir, "esgun.config.json"), `{"entry":["src/main.js"]}`)
	if err := build(dir, "named"); err == nil || !strings.Contains(err.Error(), "requires a manifest") {
		t.Fatalf("expected legacy profile-selection error, got %v", err)
	}
}
