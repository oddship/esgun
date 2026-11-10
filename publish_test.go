package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPreserveRootBuildModes(t *testing.T) {
	for _, profiled := range []bool{false, true} {
		t.Run(fmt.Sprint(profiled), func(t *testing.T) {
			dir := fixtureProject(t)
			root := filepath.Join(dir, "dist")
			mustWrite(t, filepath.Join(root, "stale/old.txt"), "stale")
			before, err := os.Stat(root)
			if err != nil {
				t.Fatal(err)
			}
			var cfg map[string]any
			if profiled {
				if err := json.Unmarshal([]byte(assertFile(t, filepath.Join(dir, "esgun.config.json"))), &cfg); err != nil {
					t.Fatal(err)
				}
			} else {
				cfg = map[string]any{"entry": []string{"src/main.js"}}
			}
			cfg["preserveRoot"] = true
			raw, _ := json.Marshal(cfg)
			mustWrite(t, filepath.Join(dir, "esgun.config.json"), string(raw))
			if err := build(dir, ""); err != nil {
				t.Fatal(err)
			}
			after, err := os.Stat(root)
			if err != nil || !os.SameFile(before, after) {
				t.Fatalf("output root replaced: %v", err)
			}
			if _, err := os.Stat(filepath.Join(root, "stale")); !os.IsNotExist(err) {
				t.Fatalf("stale output remains: %v", err)
			}
			output := "js/main.js"
			if profiled {
				output = "library.esm.js"
			}
			previous := assertFile(t, filepath.Join(root, output))
			mustWrite(t, filepath.Join(dir, "src/main.js"), "export const = invalid;")
			if err := build(dir, ""); err == nil {
				t.Fatal("invalid build succeeded")
			}
			if got := assertFile(t, filepath.Join(root, output)); got != previous {
				t.Fatal("build failure changed live output")
			}
		})
	}
}

func TestPreserveRootRollsBackPublicationFailure(t *testing.T) {
	dir := t.TempDir()
	root, stage := filepath.Join(dir, "dist"), filepath.Join(dir, "stage")
	mustWrite(t, filepath.Join(root, "old.txt"), "last good")
	mustWrite(t, filepath.Join(stage, "new.txt"), "new")
	mustWrite(t, filepath.Join(root, "private/old.txt"), "private")
	if err := os.Chmod(filepath.Join(root, "private"), 0700); err != nil {
		t.Fatal(err)
	}
	privateBefore, _ := os.Stat(filepath.Join(root, "private"))
	before, _ := os.Stat(root)
	calls := 0
	copier := func(src, dst string) error {
		calls++
		if calls == 2 {
			mustWrite(t, filepath.Join(dst, "partial.txt"), "partial")
			return fmt.Errorf("injected publication failure")
		}
		return copyPublicationTree(src, dst)
	}
	err := publishRootContents(root, stage, copier)
	if err == nil || !strings.Contains(err.Error(), "restored previous contents") {
		t.Fatalf("got %v", err)
	}
	if got := assertFile(t, filepath.Join(root, "old.txt")); got != "last good" {
		t.Fatal("previous contents not restored")
	}
	if _, err := os.Stat(filepath.Join(root, "partial.txt")); !os.IsNotExist(err) {
		t.Fatal("partial publication was not removed")
	}
	privateAfter, err := os.Stat(filepath.Join(root, "private"))
	if err != nil || privateBefore.Mode().Perm() != privateAfter.Mode().Perm() {
		t.Fatalf("rollback changed private directory permissions: %v", err)
	}
	after, _ := os.Stat(root)
	if !os.SameFile(before, after) {
		t.Fatal("rollback replaced root")
	}
	backups, _ := filepath.Glob(filepath.Join(dir, ".dist.backup-*"))
	if len(backups) != 0 {
		t.Fatal("successful rollback left backup")
	}
}

func TestPreserveRootRetainsBackupOnRollbackFailure(t *testing.T) {
	dir := t.TempDir()
	root, stage := filepath.Join(dir, "dist"), filepath.Join(dir, "stage")
	mustWrite(t, filepath.Join(root, "old.txt"), "last good")
	mustWrite(t, filepath.Join(stage, "new.txt"), "new")
	calls := 0
	err := publishRootContents(root, stage, func(src, dst string) error {
		calls++
		if calls > 1 {
			return fmt.Errorf("injected failure")
		}
		return copyPublicationTree(src, dst)
	})
	if err == nil || !strings.Contains(err.Error(), "previous output retained at") {
		t.Fatalf("got %v", err)
	}
	backups, _ := filepath.Glob(filepath.Join(dir, ".dist.backup-*"))
	if len(backups) != 1 {
		t.Fatalf("backups: %v", backups)
	}
	if !strings.Contains(err.Error(), backups[0]) {
		t.Fatal("error omitted recovery path")
	}
	if got := assertFile(t, filepath.Join(backups[0], "old.txt")); got != "last good" {
		t.Fatal("backup corrupted")
	}
}

func TestPreserveRootRejectsOutputSymlink(t *testing.T) {
	dir := t.TempDir()
	root, stage := filepath.Join(dir, "dist"), filepath.Join(dir, "stage")
	external := filepath.Join(dir, "external.txt")
	mustWrite(t, external, "do not change")
	mustWrite(t, filepath.Join(root, "old.txt"), "last good")
	mustWrite(t, filepath.Join(stage, "new.txt"), "new")
	if err := os.Symlink(external, filepath.Join(root, "link")); err != nil {
		t.Skip(err)
	}
	if err := publishOutput(root, stage, true); err == nil {
		t.Fatal("expected symlink rejection")
	}
	if assertFile(t, external) != "do not change" || assertFile(t, filepath.Join(root, "old.txt")) != "last good" {
		t.Fatal("rejection changed files")
	}
}

func TestPublicationCopyKeepsPrivateDirectoriesPrivateDuringCopy(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "source"), filepath.Join(dir, "destination")
	mustWrite(t, filepath.Join(src, "private/old.txt"), "private")
	if err := os.Chmod(filepath.Join(src, "private"), 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(filepath.Join(src, "private"), 0700); os.Chmod(filepath.Join(dst, "private"), 0700) })
	before, err := os.Stat(filepath.Join(src, "private"))
	if err != nil {
		t.Fatal(err)
	}
	copied := false
	err = copyPublicationFiles(src, dst, func(from, to string) error {
		info, err := os.Stat(filepath.Dir(to))
		if err != nil {
			return err
		}
		if info.Mode().Perm()&0077 != before.Mode().Perm()&0077 {
			t.Fatalf("private directory was widened before file copy: %v", info.Mode())
		}
		copied = true
		return copyFile(from, to)
	})
	if err != nil {
		t.Fatal(err)
	}
	if !copied {
		t.Fatal("file copy was not exercised")
	}
	after, err := os.Stat(filepath.Join(dst, "private"))
	if err != nil {
		t.Fatal(err)
	}
	if after.Mode().Perm() != before.Mode().Perm() {
		t.Fatal("final directory mode differs")
	}
}
