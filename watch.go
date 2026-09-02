package main

import (
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"path/filepath"
)

// watch rebuilds on any change under <dir>/src, debounced (mirrors build.mjs).
func watch(dir, profile string) error {
	var err error
	if dir, err = canonicalProjectDir(dir); err != nil {
		return err
	}
	cfg, _, err := loadBuildManifest(dir)
	if err != nil {
		return err
	}
	watchDirs := []string{"src"}
	if len(cfg.Watch) > 0 {
		watchDirs = cfg.Watch
	}
	root := cfg.Root
	if root == "" {
		root = "dist"
	}
	rootPath, err := safeOutputPath(dir, root, "root")
	if err != nil {
		return err
	}
	for _, watchDir := range watchDirs {
		path, err := safeProjectPath(dir, watchDir, "watch")
		if err != nil {
			return err
		}
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			return fmt.Errorf("watch path %q is not a directory", watchDir)
		}
		if pathWithin(path, rootPath) || pathWithin(rootPath, path) {
			return fmt.Errorf("watch path %q overlaps output root", watchDir)
		}
	}
	if err := build(dir, profile); err != nil {
		fmt.Fprintf(os.Stderr, "initial build failed: %v\n", err)
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer watcher.Close()

	for _, w := range watchDirs {
		if err := addRecursive(watcher, filepath.Join(dir, filepath.FromSlash(w))); err != nil {
			return err
		}
	}
	fmt.Printf("watching %s for changes…\n", watchDirs)

	var timer *time.Timer
	// mtimes of everything under the watched dirs, used by the poll fallback
	// (inotify on a host-mounted workspace may not see host-side editor writes).
	mtimes := scanMtimes(watchDirs, dir)
	poll := time.NewTicker(300 * time.Millisecond)
	defer poll.Stop()
	for {
		select {
		case ev, ok := <-watcher.Events:
			if !ok {
				return nil
			}
			if ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove|fsnotify.Rename|fsnotify.Chmod) == 0 {
				continue
			}
			scheduleBuild(&timer, dir, profile)
		case err, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			fmt.Fprintf(os.Stderr, "watcher error: %v\n", err)
		case <-poll.C:
			next := scanMtimes(watchDirs, dir)
			if !mtimesEqual(next, mtimes) {
				mtimes = next
				scheduleBuild(&timer, dir, profile)
			}
		}
	}
}

// scheduleBuild (re)starts the 100ms-debounced build timer.
var watchBuildState struct {
	sync.Mutex
	running bool
	pending bool
}

func scheduleBuild(timer **time.Timer, dir, profile string) {
	if *timer != nil {
		(*timer).Stop()
	}
	*timer = time.AfterFunc(100*time.Millisecond, func() {
		watchBuildState.Lock()
		if watchBuildState.running {
			watchBuildState.pending = true
			watchBuildState.Unlock()
			return
		}
		watchBuildState.running = true
		watchBuildState.Unlock()
		for {
			if err := build(dir, profile); err != nil {
				fmt.Fprintf(os.Stderr, "build failed: %v\n", err)
			}
			watchBuildState.Lock()
			if watchBuildState.pending {
				watchBuildState.pending = false
				watchBuildState.Unlock()
				continue
			}
			watchBuildState.running = false
			watchBuildState.Unlock()
			return
		}
	})
}

// scanMtimes returns path->mtime for every file under root.
// scanMtimes returns path->mtime for every file under the given dirs.
func scanMtimes(dirs []string, base string) map[string]int64 {
	out := map[string]int64{}
	for _, d := range dirs {
		root := filepath.Join(base, filepath.FromSlash(d))
		_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
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
		if b[k] != v {
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
