package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Exercise Listmonk's combination of entry globs, copied CSS, gzip-only output,
// and publishing into a directory that must keep its identity (a volume mount).
func TestListmonkBuildAndWatch(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "assets/js/main.js"), "export const main = 1;")
	mustWrite(t, filepath.Join(dir, "assets/js/views/list.js"), "export const view = 1;")
	mustWrite(t, filepath.Join(dir, "assets/style.css"), "body { color: red; }")
	mustWrite(t, filepath.Join(dir, "esgun.config.json"), `{
		"root":"dist", "outdir":"dist/js",
		"entry":["assets/js/main.js","assets/js/views/*.js"],
		"copies":[{"from":"assets/style.css","to":"dist/style.css"}],
		"gzip":{"extensions":[".js",".css"],"level":9,"removeOriginal":true},
		"preserveRoot":true, "watch":["assets"]
	}`)
	root := filepath.Join(dir, "dist")
	mustWrite(t, filepath.Join(root, "stale.txt"), "stale")
	before, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := build(dir, ""); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"js/main.js", "js/views/list.js", "style.css"} {
		gunzipFile(t, filepath.Join(root, name+".gz"))
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatalf("uncompressed output %s remains: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "stale.txt")); !os.IsNotExist(err) {
		t.Fatalf("stale output remains: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- watchContext(ctx, dir, "") }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("watch did not stop")
		}
	})
	// Reading raw gzip bytes here avoids assertions against a temporarily absent
	// output while preserveRoot publishes the next generation.
	css := filepath.Join(root, "style.css.gz")
	oldCSS, err := os.ReadFile(css)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(dir, "assets/style.css"), "body { color: blue; }")
	awaitWatch(t, func() bool {
		raw, err := os.ReadFile(css)
		return err == nil && string(raw) != string(oldCSS)
	})
	if got := string(gunzipFile(t, css)); !strings.Contains(got, "blue") {
		t.Fatalf("copied CSS did not update: %s", got)
	}
	probe := filepath.Join(dir, "assets/js/views/probe.js")
	output := filepath.Join(root, "js/views/probe.js.gz")
	mustWrite(t, probe, "export const probe = 1;")
	awaitWatch(t, func() bool { _, err := os.Stat(output); return err == nil })
	if err := os.Remove(probe); err != nil {
		t.Fatal(err)
	}
	awaitWatch(t, func() bool { _, err := os.Stat(output); return os.IsNotExist(err) })
	after, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("output root identity changed")
	}
}
