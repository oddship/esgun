package main

import (
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var gzipExtensionPattern = regexp.MustCompile(`^\.[A-Za-z0-9]+$`)

func validateGzip(cfg *gzipSpec) error {
	if cfg == nil {
		return nil
	}
	if len(cfg.Extensions) == 0 {
		return fmt.Errorf("gzip.extensions must not be empty")
	}
	for _, ext := range cfg.Extensions {
		if !gzipExtensionPattern.MatchString(ext) || ext == ".gz" {
			return fmt.Errorf("invalid gzip extension %q", ext)
		}
	}
	if cfg.Level != nil && (*cfg.Level < 0 || *cfg.Level > 9) {
		return fmt.Errorf("gzip.level must be between 0 and 9")
	}
	return nil
}

// compressOutput runs after all bundles and copies, before publishing the stage.
// Both legacy and profile builds use this same pipeline, including watch mode.
func compressOutput(stage string, cfg *gzipSpec) error {
	if cfg == nil {
		return nil
	}
	if err := validateGzip(cfg); err != nil {
		return err
	}
	extensions := map[string]bool{}
	for _, ext := range cfg.Extensions {
		extensions[ext] = true
	}
	owners := map[string]string{}
	names := map[string]string{}
	var selected []string
	if err := filepath.WalkDir(stage, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		names[strings.ToLower(filepath.Clean(path))] = path
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("refusing to compress non-regular output %s", path)
		}
		if err := reserveOutput(path, owners, "existing output"); err != nil {
			return err
		}
		if extensions[filepath.Ext(path)] {
			selected = append(selected, path)
		}
		return nil
	}); err != nil {
		return err
	}
	// Check every destination first, including case-folded collisions and empty
	// directories, so generated .gz files never overwrite another output.
	for _, path := range selected {
		target := path + ".gz"
		if prior, ok := names[strings.ToLower(filepath.Clean(target))]; ok {
			return fmt.Errorf("gzip output collision: %s already exists", prior)
		}
		if _, err := os.Lstat(target); err == nil {
			return fmt.Errorf("gzip output collision: %s already exists", target)
		} else if !os.IsNotExist(err) {
			return err
		}
		if err := reserveOutput(target, owners, "gzip output"); err != nil {
			return err
		}
	}
	level := gzip.BestCompression
	if cfg.Level != nil {
		level = *cfg.Level
	}
	for _, path := range selected {
		if err := gzipFile(path, level); err != nil {
			return fmt.Errorf("compressing %s: %w", path, err)
		}
		if cfg.RemoveOriginal {
			if err := os.Remove(path); err != nil {
				return err
			}
		}
	}
	return nil
}

func gzipFile(path string, level int) error {
	in, err := os.Open(path)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(path+".gz", os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return err
	}
	writer, err := gzip.NewWriterLevel(out, level)
	if err != nil {
		out.Close()
		return err
	}
	// Default gzip headers omit timestamps and filenames, producing reproducible
	// bytes without exposing source paths in the resulting assets.
	_, copyErr := io.Copy(writer, in)
	gzipErr := writer.Close()
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if gzipErr != nil {
		return gzipErr
	}
	return closeErr
}
