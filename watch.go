package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/fsnotify/fsnotify"
)

// watch rebuilds on source and manifest changes, including atomic editor saves.
func watch(dir, profile string) error {
	return watchContext(context.Background(), dir, profile)
}

func watchContext(ctx context.Context, dir, profile string) error {
	var err error
	if dir, err = canonicalProjectDir(dir); err != nil {
		return err
	}
	dirs, err := effectiveWatchDirs(dir, profile)
	if err != nil {
		return err
	}
	watcher, err := newProjectWatcher(dir, dirs)
	if err != nil {
		return err
	}
	defer func() { watcher.Close() }()

	manifest := filepath.Join(dir, "esgun.config.json")
	snapshot := func() map[string]int64 {
		return scanMtimes(append(slices.Clone(dirs), "esgun.config.json"), dir)
	}
	mtimes := snapshot()
	if err := build(dir, profile); err != nil {
		fmt.Fprintf(os.Stderr, "initial build failed: %v\n", err)
	}
	fmt.Printf("watching %s and esgun.config.json for changes…\n", dirs)

	poll := time.NewTicker(300 * time.Millisecond)
	defer poll.Stop()
	var timer *time.Timer
	var due <-chan time.Time
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	schedule := func() {
		if timer == nil {
			timer = time.NewTimer(100 * time.Millisecond)
		} else {
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(100 * time.Millisecond)
		}
		due = timer.C
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-watcher.Events:
			if !ok {
				return nil
			}
			if ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove|fsnotify.Rename|fsnotify.Chmod) == 0 {
				continue
			}
			relevant := filepath.Clean(ev.Name) == manifest
			for _, path := range dirs {
				relevant = relevant || pathWithin(filepath.Join(dir, path), ev.Name)
			}
			if relevant {
				schedule()
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			fmt.Fprintf(os.Stderr, "watcher error: %v\n", err)
		case <-poll.C:
			next := snapshot()
			if !mtimesEqual(next, mtimes) {
				mtimes = next
				schedule()
			}
		case <-due:
			due = nil
			// Reload and validate before mutating output or changing subscriptions. A
			// broken manifest keeps the last working subscriptions so edits can recover.
			next, err := effectiveWatchDirs(dir, profile)
			if err != nil {
				fmt.Fprintf(os.Stderr, "build failed: %v\n", err)
				continue
			}
			if !slices.Equal(next, dirs) {
				replacement, err := newProjectWatcher(dir, next)
				if err != nil {
					fmt.Fprintf(os.Stderr, "watcher error: %v\n", err)
					continue
				}
				watcher.Close()
				watcher = replacement
				dirs = next
			} else {
				// Newly created subdirectories need native subscriptions too. Polling
				// remains the fallback for changes made through host-mounted workspaces.
				for _, path := range dirs {
					if err := addRecursive(watcher, filepath.Join(dir, path)); err != nil {
						fmt.Fprintf(os.Stderr, "watcher error: %v\n", err)
					}
				}
			}
			// Capture before building: changes during the synchronous build trigger a
			// follow-up instead of racing another build or disappearing from the scan.
			mtimes = snapshot()
			if err := build(dir, profile); err != nil {
				fmt.Fprintf(os.Stderr, "build failed: %v\n", err)
			}
		}
	}
}

func effectiveWatchDirs(dir, profile string) ([]string, error) {
	cfg, profiled, err := loadBuildManifest(dir)
	if err != nil {
		return nil, err
	}
	if err := validateProfileSelection(cfg, profiled, profile); err != nil {
		return nil, err
	}
	dirs := cfg.Watch
	if len(dirs) == 0 {
		dirs = []string{"src"}
	}
	root := cfg.Root
	if root == "" {
		root = "dist"
	}
	output, err := safeOutputPath(dir, root, "root")
	if err != nil {
		return nil, err
	}
	for _, path := range dirs {
		resolved, err := safeProjectPath(dir, path, "watch")
		if err != nil {
			return nil, err
		}
		info, err := os.Stat(resolved)
		if err != nil || !info.IsDir() {
			return nil, fmt.Errorf("watch path %q is not a directory", path)
		}
		if pathWithin(resolved, output) || pathWithin(output, resolved) {
			return nil, fmt.Errorf("watch path %q overlaps output root", path)
		}
	}
	return dirs, nil
}

func newProjectWatcher(dir string, dirs []string) (*fsnotify.Watcher, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	// Watch the parent directory rather than the manifest inode: editors often
	// save by renaming a temporary file over esgun.config.json.
	if err := watcher.Add(dir); err != nil {
		watcher.Close()
		return nil, err
	}
	for _, path := range dirs {
		if err := addRecursive(watcher, filepath.Join(dir, path)); err != nil {
			watcher.Close()
			return nil, err
		}
	}
	return watcher, nil
}

// scanMtimes accepts directories and individual files (including the manifest).
func scanMtimes(paths []string, base string) map[string]int64 {
	out := map[string]int64{}
	for _, path := range paths {
		_ = filepath.Walk(filepath.Join(base, path), func(p string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() {
				out[p] = info.ModTime().UnixNano()
			}
			return nil
		})
	}
	return out
}

func mtimesEqual(a, b map[string]int64) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if other, ok := b[k]; !ok || other != v {
			return false
		}
	}
	return true
}

func addRecursive(w *fsnotify.Watcher, root string) error {
	return filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return w.Add(p)
		}
		return nil
	})
}
