//go:build linux

package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// A separate filesystem reproduces the EXDEV boundary of a named output volume
// without requiring Docker, privileges, or a bind mount in the test runner.
func TestPreserveRootAcrossFilesystems(t *testing.T) {
	root, err := os.MkdirTemp("/dev/shm", "esgun-publish-")
	if err != nil {
		t.Skip(err)
	}
	defer os.RemoveAll(root)
	stage := t.TempDir()
	before, _ := os.Stat(root)
	source, _ := os.Stat(stage)
	if before.Sys().(*syscall.Stat_t).Dev == source.Sys().(*syscall.Stat_t).Dev {
		t.Skip("test needs separate filesystems")
	}
	mustWrite(t, filepath.Join(root, "old.txt"), "stale")
	mustWrite(t, filepath.Join(stage, "new.txt"), "published")
	if err := publishOutput(root, stage, true); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(root)
	if !os.SameFile(before, after) {
		t.Fatal("output directory was replaced")
	}
	if assertFile(t, filepath.Join(root, "new.txt")) != "published" {
		t.Fatal("output not published")
	}
	if _, err := os.Stat(filepath.Join(root, "old.txt")); !os.IsNotExist(err) {
		t.Fatal("stale output remains")
	}
}
