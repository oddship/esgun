package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func gunzipFile(t *testing.T, path string) []byte {
	t.Helper()
	raw := []byte(assertFile(t, path))
	reader, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if reader.Name != "" || !reader.ModTime.IsZero() {
		t.Fatal("gzip leaked filename or timestamp")
	}
	out, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestGzipBuildModes(t *testing.T) {
	for _, profiled := range []bool{false, true} {
		for _, remove := range []bool{false, true} {
			name := "legacy"
			if profiled {
				name = "profiles"
			}
			t.Run(name+map[bool]string{true: "-remove", false: "-keep"}[remove], func(t *testing.T) {
				dir := fixtureProject(t)
				config := filepath.Join(dir, "esgun.config.json")
				var cfg map[string]any
				if profiled {
					if err := json.Unmarshal([]byte(assertFile(t, config)), &cfg); err != nil {
						t.Fatal(err)
					}
				} else {
					cfg = map[string]any{"entry": []string{"src/main.js"}, "copies": []map[string]string{{"from": "assets", "to": "dist/assets"}}}
				}
				mustWrite(t, filepath.Join(dir, "assets/logo.svg"), "<svg>asset</svg>")
				mustWrite(t, filepath.Join(dir, "assets/font.woff2"), "font bytes")
				raw, _ := json.Marshal(cfg)
				mustWrite(t, config, string(raw))
				if err := build(dir, ""); err != nil {
					t.Fatal(err)
				}
				expected := map[string][]byte{}
				_ = filepath.WalkDir(filepath.Join(dir, "dist"), func(path string, e os.DirEntry, err error) error {
					if err != nil {
						return err
					}
					if !e.IsDir() {
						expected[path] = []byte(assertFile(t, path))
					}
					return nil
				})
				cfg["gzip"] = map[string]any{"extensions": []string{".js", ".css", ".svg"}, "removeOriginal": remove}
				raw, _ = json.Marshal(cfg)
				mustWrite(t, config, string(raw))
				if err := build(dir, ""); err != nil {
					t.Fatal(err)
				}
				compressed := map[string][]byte{}
				for path, want := range expected {
					switch filepath.Ext(path) {
					case ".js", ".css", ".svg":
						if got := gunzipFile(t, path+".gz"); !bytes.Equal(got, want) {
							t.Fatalf("decoded output changed: %s", path)
						}
						compressed[path] = []byte(assertFile(t, path+".gz"))
						if remove {
							if _, err := os.Stat(path); !os.IsNotExist(err) {
								t.Fatalf("original still exists: %s", path)
							}
						} else if got := assertFile(t, path); got != string(want) {
							t.Fatal("original changed")
						}
					default:
						if got := assertFile(t, path); got != string(want) {
							t.Fatal("unselected asset changed")
						}
					}
				}
				if err := build(dir, ""); err != nil {
					t.Fatal(err)
				}
				for path, want := range compressed {
					if got := []byte(assertFile(t, path+".gz")); !bytes.Equal(got, want) {
						t.Fatalf("gzip bytes are not repeatable: %s", path)
					}
				}
			})
		}
	}
}

func TestGzipRejectsInvalidSettingsAndCollisions(t *testing.T) {
	for _, setting := range []string{`{"extensions":[]}`, `{"extensions":["js"]}`, `{"extensions":["../x"]}`, `{"extensions":[".gz"]}`, `{"extensions":[".js"],"level":10}`, `{"extensions":[".js"],"level":-1}`} {
		t.Run(setting, func(t *testing.T) {
			dir := fixtureProject(t)
			mustWrite(t, filepath.Join(dir, "dist/keep.txt"), "last good")
			mustWrite(t, filepath.Join(dir, "esgun.config.json"), `{"entry":["src/main.js"],"gzip":`+setting+`}`)
			if err := build(dir, ""); err == nil {
				t.Fatal("expected invalid gzip settings to fail")
			}
			if assertFile(t, filepath.Join(dir, "dist/keep.txt")) != "last good" {
				t.Fatal("live output changed")
			}
		})
	}
	for _, collision := range []string{"main.js.gz", "main.js.GZ"} {
		t.Run(collision, func(t *testing.T) {
			dir := fixtureProject(t)
			mustWrite(t, filepath.Join(dir, "dist/keep.txt"), "last good")
			mustWrite(t, filepath.Join(dir, "assets", collision), "existing compressed asset")
			mustWrite(t, filepath.Join(dir, "esgun.config.json"), `{"entry":["src/main.js"],"copies":[{"from":"assets/`+collision+`","to":"dist/js/`+collision+`"}],"gzip":{"extensions":[".js"]}}`)
			if err := build(dir, ""); err == nil || !strings.Contains(err.Error(), "collision") {
				t.Fatalf("expected collision, got %v", err)
			}
			if assertFile(t, filepath.Join(dir, "dist/keep.txt")) != "last good" {
				t.Fatal("live output changed")
			}
		})
	}
}

func TestGzipExplicitZeroLevel(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.js")
	mustWrite(t, path, "hello")
	zero := 0
	if err := compressOutput(dir, &gzipSpec{Extensions: []string{".js"}, Level: &zero}); err != nil {
		t.Fatal(err)
	}
	if string(gunzipFile(t, path+".gz")) != "hello" {
		t.Fatal("zero-level gzip changed payload")
	}
}

func TestGzipPreservesPermissionsAndRejectsDirectoryCollision(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "private.js")
	mustWrite(t, path, "private")
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := compressOutput(dir, &gzipSpec{Extensions: []string{".js"}}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path + ".gz")
	if err != nil || info.Mode().Perm() != before.Mode().Perm() {
		t.Fatalf("gzip widened permissions: %v, %v", info, err)
	}
	dir = t.TempDir()
	path = filepath.Join(dir, "main.js")
	mustWrite(t, path, "payload")
	if err := os.Mkdir(path+".GZ", 0755); err != nil {
		t.Fatal(err)
	}
	if err := compressOutput(dir, &gzipSpec{Extensions: []string{".js"}}); err == nil || !strings.Contains(err.Error(), "collision") {
		t.Fatalf("expected empty directory collision, got %v", err)
	}
}
