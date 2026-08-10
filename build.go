package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"encoding/json"
	"github.com/evanw/esbuild/pkg/api"
	"path/filepath"
)

func build(dir string) error {
	var err error
	if dir, err = filepath.Abs(dir); err != nil {
		return err
	}
	cfg, err := loadBuildManifest(dir)
	if err != nil {
		return err
	}
	outRoot := cfg.Root
	if outRoot == "" {
		outRoot = "dist"
	}
	outdir := cfg.Outdir
	if outdir == "" {
		outdir = filepath.Join(outRoot, "js")
	}

	if err := removeAllRetry(filepath.Join(dir, outRoot)); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(dir, outdir), 0o755); err != nil {
		return err
	}

	// Expand entry points (explicit paths and single-level globs).
	entrypoints, err := expandEntrypoints(dir, cfg.Entry)
	if err != nil {
		return err
	}
	if len(entrypoints) == 0 {
		return fmt.Errorf("no entry points matched %v", cfg.Entry)
	}

	nodePaths := []string{}
	for _, r := range cfg.Resolve {
		nodePaths = append(nodePaths, filepath.Join(dir, filepath.FromSlash(r)))
	}
	result := api.Build(api.BuildOptions{
		AbsWorkingDir:     dir,
		EntryPoints:       entrypoints,
		Outdir:            filepath.ToSlash(outdir),
		Bundle:            true,
		Format:            api.FormatESModule,
		Splitting:         true,
		MinifyWhitespace:  true,
		MinifyIdentifiers: true,
		MinifySyntax:      true,
		Target:            api.ES2020,
		EntryNames:        "[dir]/[name]",
		ChunkNames:        "chunks/[name]-[hash]",
		NodePaths:         nodePaths,
		Write:             true,
		LogLevel:          api.LogLevelInfo,
	})
	if len(result.Errors) > 0 {
		for _, e := range result.Errors {
			fmt.Fprintf(os.Stderr, "esbuild error: %s\n", e.Text)
		}
		return fmt.Errorf("bundle failed with %d errors", len(result.Errors))
	}
	fmt.Printf("bundled %d entry points -> %d outputs\n", len(entrypoints), len(result.OutputFiles))

	// Verbatim copies (files or whole directories).
	for _, c := range cfg.Copies {
		src := filepath.Join(dir, filepath.FromSlash(c.From))
		dst := filepath.Join(dir, filepath.FromSlash(c.To))
		info, err := os.Stat(src)
		if err != nil {
			return fmt.Errorf("copy %s: %w", c.From, err)
		}
		if info.IsDir() {
			if err := copyTree(src, dst); err != nil {
				return fmt.Errorf("copy %s: %w", c.From, err)
			}
		} else if err := copyFile(src, dst); err != nil {
			return fmt.Errorf("copy %s: %w", c.From, err)
		}
	}

	// Curated tree copies (e.g. a subset of TinyMCE).
	for _, t := range cfg.TreeCopies {
		base := filepath.Join(dir, filepath.FromSlash(t.From))
		for _, inc := range t.Include {
			src := filepath.Join(base, filepath.FromSlash(inc))
			dst := filepath.Join(dir, filepath.FromSlash(t.To), filepath.FromSlash(inc))
			info, err := os.Stat(src)
			if err != nil {
				return fmt.Errorf("treeCopy %s/%s: %w", t.From, inc, err)
			}
			if info.IsDir() {
				if err := copyTree(src, dst); err != nil {
					return fmt.Errorf("treeCopy %s/%s: %w", t.From, inc, err)
				}
			} else if err := copyFile(src, dst); err != nil {
				return fmt.Errorf("treeCopy %s/%s: %w", t.From, inc, err)
			}
		}
	}

	fmt.Printf("dist ready: %s\n", filepath.Join(dir, outRoot))
	return nil
}

// expandEntrypoints resolves a mix of explicit paths and globs to files.
func expandEntrypoints(dir string, specs []string) ([]string, error) {
	out := []string{}
	seen := map[string]bool{}
	for _, s := range specs {
		s = filepath.ToSlash(s)
		matches := []string{}
		if strings.ContainsAny(s, "*?[") {
			ms, err := filepath.Glob(filepath.Join(dir, filepath.FromSlash(s)))
			if err != nil {
				return nil, err
			}
			for _, m := range ms {
				rel, err := filepath.Rel(dir, m)
				if err != nil {
					return nil, err
				}
				matches = append(matches, filepath.ToSlash(rel))
			}
		} else if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(s))); err == nil {
			matches = append(matches, s)
		}
		for _, m := range matches {
			if !seen[m] {
				seen[m] = true
				out = append(out, m)
			}
		}
	}
	return out, nil
}

// loadBuildManifest reads the esgun.config.json build manifest next to package.json.
func loadBuildManifest(dir string) (*buildManifest, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "esgun.config.json"))
	if err != nil {
		return nil, fmt.Errorf("reading esgun.config.json (run 'esgun deps' and create the build manifest): %w", err)
	}
	var cfg buildManifest
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parsing esgun.config.json: %w", err)
	}
	return &cfg, nil
}

// removeAllRetry removes a tree, retrying on transient mount errors (e.g.
// "directory not empty" from virtiofs/9p directory-entry caching).
func removeAllRetry(p string) error {
	var err error
	for i := 0; i < 5; i++ {
		err = os.RemoveAll(p)
		if err == nil {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return err
}

func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func copyTree(src, dst string) error {
	return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(p, target)
	})
}
