package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func awaitWatch(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("watch output did not update before deadline")
}

func TestWatchReloadsManifestAndRecovers(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "src/main.js"), "export const value = 1;")
	mustWrite(t, filepath.Join(dir, "assets/note.txt"), "first")
	config := filepath.Join(dir, "esgun.config.json")
	cfg := map[string]any{"root": "dist", "entry": []string{"src/main.js"}}
	save := func(atomic bool) {
		t.Helper()
		raw, err := json.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if atomic {
			mustWrite(t, config+".tmp", string(raw))
			if err := os.Rename(config+".tmp", config); err != nil {
				t.Fatal(err)
			}
		} else {
			mustWrite(t, config, string(raw))
		}
	}
	save(false)
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
	output := filepath.Join(dir, "dist/assets/note.txt")
	awaitWatch(t, func() bool { _, err := os.Stat(filepath.Join(dir, "dist/js/main.js")); return err == nil })
	// Config-only atomic replacement must add the copy without a source edit.
	cfg["copies"] = []map[string]string{{"from": "assets", "to": "dist/assets"}}
	cfg["watch"] = []string{"src", "assets"}
	save(true)
	awaitWatch(t, func() bool { raw, _ := os.ReadFile(output); return string(raw) == "first" })
	// The new watch directory is active, including directories created later.
	mustWrite(t, filepath.Join(dir, "assets/nested/value.txt"), "nested")
	awaitWatch(t, func() bool {
		raw, _ := os.ReadFile(filepath.Join(dir, "dist/assets/nested/value.txt"))
		return string(raw) == "nested"
	})
	mustWrite(t, filepath.Join(dir, "assets/nested/value.txt"), "edited nested")
	awaitWatch(t, func() bool {
		raw, _ := os.ReadFile(filepath.Join(dir, "dist/assets/nested/value.txt"))
		return string(raw) == "edited nested"
	})
	mustWrite(t, filepath.Join(dir, "assets/note.txt"), "second")
	awaitWatch(t, func() bool { raw, _ := os.ReadFile(output); return string(raw) == "second" })
	// Invalid JSON must not destroy output, and a corrected config must recover.
	mustWrite(t, config, `{"root":`)
	time.Sleep(450 * time.Millisecond)
	if got := assertFile(t, output); got != "second" {
		t.Fatalf("invalid config changed output: %s", got)
	}
	cfg["copies"] = []map[string]string{{"from": "assets/note.txt", "to": "dist/recovered.txt"}}
	save(false)
	awaitWatch(t, func() bool {
		raw, _ := os.ReadFile(filepath.Join(dir, "dist/recovered.txt"))
		return string(raw) == "second"
	})
	// Changing root into a watched directory is rejected before output mutation.
	cfg["root"] = "src/generated"
	cfg["copies"] = []map[string]string{{"from": "assets/note.txt", "to": "src/generated/recovered.txt"}}
	save(true)
	time.Sleep(450 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(dir, "src/generated")); !os.IsNotExist(err) {
		t.Fatalf("unsafe watch root was built: %v", err)
	}
	cfg["root"] = "dist"
	cfg["copies"] = []map[string]string{{"from": "assets/note.txt", "to": "dist/recovered.txt"}}
	save(true)
	mustWrite(t, filepath.Join(dir, "assets/note.txt"), "third")
	awaitWatch(t, func() bool {
		raw, _ := os.ReadFile(filepath.Join(dir, "dist/recovered.txt"))
		return string(raw) == "third"
	})
}

func TestEffectiveWatchDirsRevalidatesSelectedProfile(t *testing.T) {
	dir := fixtureProject(t)
	if _, err := effectiveWatchDirs(dir, "missing"); err == nil || !strings.Contains(err.Error(), "unknown profile") {
		t.Fatalf("got %v", err)
	}
}
