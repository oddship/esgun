package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/evanw/esbuild/pkg/api"
)

var configNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
var suffixPattern = regexp.MustCompile(`^\.[A-Za-z0-9_-]+$`)

type expandedEntry struct {
	Input  string
	Output string
}

type preparedProfile struct {
	Name        string
	Config      buildProfile
	Outdir      string
	Entrypoints []expandedEntry
}

// build assembles either the legacy single-build manifest or selected named
// profiles. An empty selectedProfile builds every profile in lexical order.
func build(dir, selectedProfile string) error {
	projectDir, err := canonicalProjectDir(dir)
	if err != nil {
		return err
	}
	cfg, profiled, err := loadBuildManifest(projectDir)
	if err != nil {
		return err
	}
	if err := validateProfileSelection(cfg, profiled, selectedProfile); err != nil {
		return err
	}
	if cfg.Target != "" && cfg.Target != "es2020" && cfg.Target != "esnext" {
		return fmt.Errorf("target must be es2020 or esnext")
	}
	if err := validateGzip(cfg.Gzip); err != nil {
		return err
	}
	outRoot := cfg.Root
	if outRoot == "" {
		outRoot = "dist"
	}
	rootPath, err := safeOutputPath(projectDir, outRoot, "root")
	if err != nil {
		return err
	}
	if rootPath == projectDir {
		return fmt.Errorf("root must not be the project directory")
	}
	if err := validateSharedPaths(projectDir, rootPath, cfg); err != nil {
		return err
	}
	if !profiled {
		return buildLegacy(projectDir, rootPath, cfg)
	}
	profiles, err := prepareProfiles(projectDir, rootPath, cfg, selectedProfile)
	if err != nil {
		return err
	}
	if err := detectProfileCollisions(profiles); err != nil {
		return err
	}

	// All validation completes before the first mutation. Clean once, execute
	// profiles serially, and perform global copies once at the end.
	if err := buildProfilesStaged(projectDir, rootPath, cfg, profiles); err != nil {
		return err
	}
	fmt.Printf("dist ready: %s\n", rootPath)
	return nil
}

func validateProfileSelection(cfg *buildManifest, profiled bool, selected string) error {
	if selected == "" {
		return nil
	}
	if !profiled {
		return fmt.Errorf("--profile requires a manifest with named profiles")
	}
	if _, ok := cfg.Profiles[selected]; !ok {
		return fmt.Errorf("unknown profile %q", selected)
	}
	return nil
}

func buildLegacy(dir, rootPath string, cfg *buildManifest) error {
	outdir := cfg.Outdir
	if outdir == "" {
		outdir = filepath.Join("dist", "js")
		if cfg.Root != "" {
			outdir = filepath.Join(cfg.Root, "js")
		}
	}
	outdirPath, err := safeOutputPath(dir, outdir, "outdir")
	if err != nil {
		return err
	}
	if !pathWithin(rootPath, outdirPath) {
		return fmt.Errorf("outdir %q must be inside root", outdir)
	}
	entrypoints, err := expandEntrypoints(dir, cfg.Entry)
	if err != nil {
		return err
	}
	if len(entrypoints) == 0 {
		return fmt.Errorf("no entry points matched %v", cfg.Entry)
	}
	stage, err := newStagingRoot(rootPath)
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	stageOutdir, err := remapOutput(rootPath, stage, outdirPath)
	if err != nil {
		return err
	}
	result := api.Build(api.BuildOptions{
		AbsWorkingDir: dir, EntryPoints: entrypoints, Outdir: stageOutdir,
		Bundle: true, Format: api.FormatESModule, Splitting: true,
		MinifyWhitespace: true, MinifyIdentifiers: true, MinifySyntax: true,
		Target: buildTarget(cfg.Target), EntryNames: "[dir]/[name]", ChunkNames: "chunks/[name]-[hash]",
		NodePaths: resolvePaths(dir, cfg.Resolve), Write: false, LogLevel: api.LogLevelInfo,
	})
	if err := buildResultError(result); err != nil {
		return err
	}
	owners := map[string]string{}
	if err := writeBuildOutputs(result.OutputFiles, owners, "legacy build"); err != nil {
		return err
	}
	fmt.Printf("bundled %d entry points\n", len(entrypoints))
	if err := runCopies(dir, rootPath, stage, cfg, owners); err != nil {
		return err
	}
	if err := compressOutput(stage, cfg.Gzip); err != nil {
		return err
	}
	if err := publishOutput(rootPath, stage, cfg.PreserveRoot); err != nil {
		return err
	}
	fmt.Printf("dist ready: %s\n", rootPath)
	return nil
}

func prepareProfiles(dir, rootPath string, cfg *buildManifest, selected string) ([]preparedProfile, error) {
	if len(cfg.Entry) > 0 || cfg.Outdir != "" {
		return nil, fmt.Errorf("entry/outdir cannot be mixed with entrySets/profiles")
	}
	if len(cfg.EntrySets) == 0 || len(cfg.Profiles) == 0 {
		return nil, fmt.Errorf("profile mode requires non-empty entrySets and profiles")
	}
	setNames := make([]string, 0, len(cfg.EntrySets))
	for name := range cfg.EntrySets {
		setNames = append(setNames, name)
	}
	sort.Strings(setNames)
	for _, name := range setNames {
		entries := cfg.EntrySets[name]
		if !configNamePattern.MatchString(name) || len(entries) == 0 {
			return nil, fmt.Errorf("invalid or empty entry set %q", name)
		}
		for i, entry := range entries {
			if err := validateEntrySpec(name, i, entry); err != nil {
				return nil, err
			}
		}
	}

	names := make([]string, 0, len(cfg.Profiles))
	if selected != "" {
		if _, ok := cfg.Profiles[selected]; !ok {
			return nil, fmt.Errorf("unknown profile %q", selected)
		}
		names = append(names, selected)
	} else {
		for name := range cfg.Profiles {
			names = append(names, name)
		}
		sort.Strings(names)
	}
	prepared := make([]preparedProfile, 0, len(names))
	for _, name := range names {
		profile := cfg.Profiles[name]
		if err := validateProfile(name, profile); err != nil {
			return nil, err
		}
		outdir, err := safeOutputPath(dir, profile.Outdir, "profile "+name+" outdir")
		if err != nil {
			return nil, err
		}
		if !pathWithin(rootPath, outdir) {
			return nil, fmt.Errorf("profile %q outdir %q must be inside root", name, profile.Outdir)
		}
		entries := []expandedEntry{}
		for _, setName := range profile.EntrySets {
			set, ok := cfg.EntrySets[setName]
			if !ok {
				return nil, fmt.Errorf("profile %q references unknown entry set %q", name, setName)
			}
			expanded, err := expandNamedEntries(dir, set)
			if err != nil {
				return nil, fmt.Errorf("profile %q: %w", name, err)
			}
			entries = append(entries, expanded...)
		}
		if len(entries) == 0 {
			return nil, fmt.Errorf("profile %q has no matching entry points", name)
		}
		prepared = append(prepared, preparedProfile{Name: name, Config: profile, Outdir: outdir, Entrypoints: entries})
	}
	return prepared, nil
}

func validateProfile(name string, profile buildProfile) error {
	if !configNamePattern.MatchString(name) {
		return fmt.Errorf("invalid profile name %q", name)
	}
	if len(profile.EntrySets) == 0 || profile.Outdir == "" {
		return fmt.Errorf("profile %q requires entrySets and outdir", name)
	}
	seen := map[string]bool{}
	for _, setName := range profile.EntrySets {
		if !configNamePattern.MatchString(setName) || seen[setName] {
			return fmt.Errorf("profile %q has invalid or repeated entry set %q", name, setName)
		}
		seen[setName] = true
	}
	if profile.Format != "esm" && profile.Format != "iife" {
		return fmt.Errorf("profile %q format must be esm or iife", name)
	}
	if profile.Format == "iife" && profile.Splitting {
		return fmt.Errorf("profile %q cannot combine iife format with splitting", name)
	}
	if profile.Target != "" && profile.Target != "es2020" && profile.Target != "esnext" {
		return fmt.Errorf("profile %q target must be es2020 or esnext", name)
	}
	if profile.Suffix != "" && !suffixPattern.MatchString(profile.Suffix) {
		return fmt.Errorf("profile %q has invalid suffix %q", name, profile.Suffix)
	}
	return nil
}

func validateEntrySpec(setName string, index int, entry entrySpec) error {
	if entry.Input == "" || entry.Output == "" {
		return fmt.Errorf("entry set %q item %d requires input and output", setName, index)
	}
	if err := validateRelativePath(entry.Input, "entry input"); err != nil {
		return err
	}
	if err := validateRelativePath(entry.Output, "entry output"); err != nil {
		return err
	}
	if strings.Contains(entry.Output, `\`) {
		return fmt.Errorf("entry set %q output %q must use forward slashes", setName, entry.Output)
	}
	if strings.ContainsAny(entry.Output, "*?") || strings.ContainsAny(strings.ReplaceAll(entry.Output, "[name]", ""), "[]") {
		return fmt.Errorf("entry set %q output %q may only use the [name] placeholder", setName, entry.Output)
	}
	if strings.Count(entry.Output, "[name]") > 1 {
		return fmt.Errorf("entry set %q output %q repeats [name]", setName, entry.Output)
	}
	if strings.ContainsAny(entry.Input, "*?[") && strings.Count(entry.Output, "[name]") != 1 {
		return fmt.Errorf("entry set %q glob input %q requires one [name] in output", setName, entry.Input)
	}
	if filepath.Ext(entry.Output) != "" {
		return fmt.Errorf("entry set %q output %q must omit its extension", setName, entry.Output)
	}
	return nil
}

func expandNamedEntries(dir string, specs []entrySpec) ([]expandedEntry, error) {
	entries := []expandedEntry{}
	for _, spec := range specs {
		matches, err := expandEntrypoints(dir, []string{spec.Input})
		if err != nil {
			return nil, err
		}
		if len(matches) == 0 {
			return nil, fmt.Errorf("entry input %q matched no files", spec.Input)
		}
		for _, input := range matches {
			name := strings.TrimSuffix(filepath.Base(input), filepath.Ext(input))
			output := strings.ReplaceAll(spec.Output, "[name]", name)
			entries = append(entries, expandedEntry{Input: input, Output: filepath.ToSlash(output)})
		}
	}
	return entries, nil
}

func detectProfileCollisions(profiles []preparedProfile) error {
	owners := map[string]string{}
	chunks := map[string]string{}
	for _, profile := range profiles {
		for _, entry := range profile.Entrypoints {
			ext := ".js"
			if strings.EqualFold(filepath.Ext(entry.Input), ".css") {
				ext = ".css"
			}
			output := filepath.Join(profile.Outdir, filepath.FromSlash(entry.Output+profile.Config.Suffix+ext))
			key := strings.ToLower(filepath.Clean(output))
			if prior, ok := owners[key]; ok {
				return fmt.Errorf("output collision: profiles %q and %q both write %s", prior, profile.Name, output)
			}
			owners[key] = profile.Name
		}
		if profile.Config.Splitting {
			key := strings.ToLower(filepath.Join(profile.Outdir, "chunks", "*"+profile.Config.Suffix+".js"))
			if prior, ok := chunks[key]; ok {
				return fmt.Errorf("chunk output collision: profiles %q and %q share %s", prior, profile.Name, key)
			}
			chunks[key] = profile.Name
		}
	}
	return nil
}

func runProfile(dir string, cfg *buildManifest, profile preparedProfile) ([]api.OutputFile, error) {
	if err := os.MkdirAll(profile.Outdir, 0o755); err != nil {
		return nil, err
	}
	entrypoints := make([]api.EntryPoint, 0, len(profile.Entrypoints))
	for _, entry := range profile.Entrypoints {
		entrypoints = append(entrypoints, api.EntryPoint{InputPath: entry.Input, OutputPath: entry.Output})
	}
	format := api.FormatESModule
	if profile.Config.Format == "iife" {
		format = api.FormatIIFE
	}
	outExtensions := map[string]string{}
	if profile.Config.Suffix != "" {
		outExtensions[".js"] = profile.Config.Suffix + ".js"
		outExtensions[".css"] = profile.Config.Suffix + ".css"
	}
	target := profile.Config.Target
	if target == "" {
		target = cfg.Target
	}
	result := api.Build(api.BuildOptions{
		AbsWorkingDir: dir, EntryPointsAdvanced: entrypoints, Outdir: profile.Outdir,
		OutExtension: outExtensions, Bundle: true, Format: format, Splitting: profile.Config.Splitting,
		MinifyWhitespace: profile.Config.Minify, MinifyIdentifiers: profile.Config.Minify, MinifySyntax: profile.Config.Minify,
		Target: buildTarget(target), ChunkNames: "chunks/[name]-[hash]", NodePaths: resolvePaths(dir, cfg.Resolve),
		Write: false, LogLevel: api.LogLevelInfo,
	})
	if err := buildResultError(result); err != nil {
		return nil, err
	}
	fmt.Printf("profile %s: bundled %d entry points\n", profile.Name, len(entrypoints))
	return result.OutputFiles, nil
}

func buildTarget(target string) api.Target {
	if target == "esnext" {
		return api.ESNext
	}
	return api.ES2020
}

func buildProfilesStaged(dir, rootPath string, cfg *buildManifest, profiles []preparedProfile) error {
	stage, err := newStagingRoot(rootPath)
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	owners := map[string]string{}
	for _, profile := range profiles {
		staged := profile
		staged.Outdir, err = remapOutput(rootPath, stage, profile.Outdir)
		if err != nil {
			return err
		}
		outputs, err := runProfile(dir, cfg, staged)
		if err != nil {
			return fmt.Errorf("profile %q: %w", profile.Name, err)
		}
		if err := writeBuildOutputs(outputs, owners, "profile "+profile.Name); err != nil {
			return err
		}
	}
	if err := runCopies(dir, rootPath, stage, cfg, owners); err != nil {
		return err
	}
	if err := compressOutput(stage, cfg.Gzip); err != nil {
		return err
	}
	return publishOutput(rootPath, stage, cfg.PreserveRoot)
}

func writeBuildOutputs(outputs []api.OutputFile, owners map[string]string, owner string) error {
	for _, output := range outputs {
		if err := reserveOutput(output.Path, owners, owner); err != nil {
			return err
		}
	}
	for _, output := range outputs {
		if err := os.MkdirAll(filepath.Dir(output.Path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(output.Path, output.Contents, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func newStagingRoot(rootPath string) (string, error) {
	parent := filepath.Dir(rootPath)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", err
	}
	stage, err := os.MkdirTemp(parent, "."+filepath.Base(rootPath)+".esgun-")
	if err != nil {
		return "", err
	}
	if err := os.Chmod(stage, 0o755); err != nil {
		_ = os.Remove(stage)
		return "", err
	}
	return stage, nil
}

func remapOutput(rootPath, stage, output string) (string, error) {
	rel, err := filepath.Rel(rootPath, output)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("output %s is outside root %s", output, rootPath)
	}
	return filepath.Join(stage, rel), nil
}

func replaceOutputRoot(rootPath, stage string) error {
	backup, err := os.MkdirTemp(filepath.Dir(rootPath), "."+filepath.Base(rootPath)+".backup-")
	if err != nil {
		return err
	}
	if err := os.Remove(backup); err != nil {
		return err
	}
	hadRoot := false
	if _, err := os.Lstat(rootPath); err == nil {
		hadRoot = true
		if err := os.Rename(rootPath, backup); err != nil {
			return fmt.Errorf("backing up existing output: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(stage, rootPath); err != nil {
		if hadRoot {
			if rollbackErr := os.Rename(backup, rootPath); rollbackErr != nil {
				return fmt.Errorf("activating staged output failed and restoring the previous output from %s also failed: %w", backup, errors.Join(err, rollbackErr))
			}
		}
		return fmt.Errorf("activating staged output: %w", err)
	}
	if hadRoot {
		if err := os.RemoveAll(backup); err != nil {
			return fmt.Errorf("removing output backup: %w", err)
		}
	}
	return nil
}

func buildResultError(result api.BuildResult) error {
	if len(result.Errors) == 0 {
		return nil
	}
	for _, buildErr := range result.Errors {
		fmt.Fprintf(os.Stderr, "esbuild error: %s\n", buildErr.Text)
	}
	return fmt.Errorf("bundle failed with %d errors", len(result.Errors))
}

func resolvePaths(dir string, specs []string) []string {
	paths := make([]string, 0, len(specs))
	for _, spec := range specs {
		paths = append(paths, filepath.Join(dir, filepath.FromSlash(spec)))
	}
	return paths
}

// expandEntrypoints resolves explicit paths and globs to project-local files.
func expandEntrypoints(dir string, specs []string) ([]string, error) {
	out := []string{}
	seen := map[string]bool{}
	for _, spec := range specs {
		if err := validateRelativePath(spec, "entry"); err != nil {
			return nil, err
		}
		matches := []string{}
		if strings.ContainsAny(spec, "*?[") {
			matched, err := filepath.Glob(filepath.Join(dir, filepath.FromSlash(spec)))
			if err != nil {
				return nil, err
			}
			for _, match := range matched {
				resolved, err := ensureResolvedWithin(dir, match, "entry")
				if err != nil {
					return nil, err
				}
				if info, err := os.Stat(resolved); err != nil || !info.Mode().IsRegular() {
					return nil, fmt.Errorf("entry %q is not a regular file", match)
				}
				rel, err := filepath.Rel(dir, match)
				if err != nil {
					return nil, err
				}
				matches = append(matches, filepath.ToSlash(rel))
			}
		} else {
			path, err := safeProjectPath(dir, spec, "entry")
			if err != nil {
				return nil, err
			}
			if info, statErr := os.Stat(path); statErr == nil && info.Mode().IsRegular() {
				matches = append(matches, filepath.ToSlash(spec))
			}
		}
		sort.Strings(matches)
		for _, match := range matches {
			if !seen[match] {
				seen[match] = true
				out = append(out, match)
			}
		}
	}
	return out, nil
}

// New profile-mode manifests reject unknown fields; legacy manifests remain
// permissive to preserve their historical behavior.
func loadBuildManifest(dir string) (*buildManifest, bool, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "esgun.config.json"))
	if err != nil {
		return nil, false, fmt.Errorf("reading esgun.config.json (run 'esgun deps' and create the build manifest): %w", err)
	}
	var marker map[string]json.RawMessage
	if err := json.Unmarshal(raw, &marker); err != nil {
		return nil, false, fmt.Errorf("parsing esgun.config.json: %w", err)
	}
	_, hasEntrySets := marker["entrySets"]
	_, hasProfiles := marker["profiles"]
	profiled := hasEntrySets || hasProfiles
	var cfg buildManifest
	if profiled {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&cfg); err != nil {
			return nil, false, fmt.Errorf("parsing esgun.config.json: %w", err)
		}
	} else if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, false, fmt.Errorf("parsing esgun.config.json: %w", err)
	}
	return &cfg, profiled, nil
}

func canonicalProjectDir(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolving project directory: %w", err)
	}
	info, err := os.Stat(real)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("project directory %q is not a directory", dir)
	}
	return filepath.Clean(real), nil
}

func validateSharedPaths(dir, rootPath string, cfg *buildManifest) error {
	for _, resolve := range cfg.Resolve {
		path, err := safeProjectPath(dir, resolve, "resolve")
		if err != nil {
			return err
		}
		if info, err := os.Stat(path); err != nil || !info.IsDir() {
			return fmt.Errorf("resolve path %q is not a directory", resolve)
		}
	}
	for _, copy := range cfg.Copies {
		source, err := safeOutputPath(dir, copy.From, "copy source")
		if err != nil {
			return err
		}
		if err := validateCopySource(source, "copy source "+copy.From); err != nil {
			return err
		}
		target, err := safeOutputPath(dir, copy.To, "copy target")
		if err != nil {
			return err
		}
		if !pathWithin(rootPath, target) {
			return fmt.Errorf("copy target %q must be inside root", copy.To)
		}
		if pathWithin(source, target) || pathWithin(target, source) || pathWithin(rootPath, source) {
			return fmt.Errorf("copy %q -> %q overlaps its source or output root", copy.From, copy.To)
		}
	}
	for _, treeCopy := range cfg.TreeCopies {
		base, err := safeOutputPath(dir, treeCopy.From, "treeCopy source")
		if err != nil {
			return err
		}
		if err := validateCopySource(base, "treeCopy source "+treeCopy.From); err != nil {
			return err
		}
		target, err := safeOutputPath(dir, treeCopy.To, "treeCopy target")
		if err != nil {
			return err
		}
		if !pathWithin(rootPath, target) {
			return fmt.Errorf("treeCopy target %q must be inside root", treeCopy.To)
		}
		if pathWithin(base, target) || pathWithin(target, base) || pathWithin(rootPath, base) {
			return fmt.Errorf("treeCopy %q -> %q overlaps its source or output root", treeCopy.From, treeCopy.To)
		}
		for _, include := range treeCopy.Include {
			source, err := safeOutputPath(base, include, "treeCopy include")
			if err != nil {
				return err
			}
			if err := validateCopySource(source, "treeCopy include "+include); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateCopySource(source, label string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	if info.Mode().IsRegular() {
		return nil
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a regular file or directory", label)
	}
	return filepath.Walk(source, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || info.Mode().IsRegular() {
			return nil
		}
		return fmt.Errorf("%s contains non-regular file %s", label, path)
	})
}

func validateRelativePath(value, label string) error {
	if value == "" || strings.ContainsRune(value, 0) {
		return fmt.Errorf("%s path is empty or invalid", label)
	}
	path := filepath.FromSlash(value)
	if filepath.IsAbs(path) || filepath.VolumeName(path) != "" {
		return fmt.Errorf("%s path %q must be relative", label, value)
	}
	clean := filepath.Clean(path)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%s path %q escapes the project", label, value)
	}
	return nil
}

func safeProjectPath(base, rel, label string) (string, error) {
	if err := validateRelativePath(rel, label); err != nil {
		return "", err
	}
	target := filepath.Clean(filepath.Join(base, filepath.FromSlash(rel)))
	return ensureResolvedWithin(base, target, label)
}

// safeOutputPath rejects symlinks in every existing destination component.
// Returning a resolved symlink target is safe for reads but dangerous for a
// path that may be deleted or overwritten.
func safeOutputPath(base, rel, label string) (string, error) {
	if err := validateRelativePath(rel, label); err != nil {
		return "", err
	}
	target := filepath.Clean(filepath.Join(base, filepath.FromSlash(rel)))
	if !pathWithin(base, target) {
		return "", fmt.Errorf("%s path %q escapes the project", label, rel)
	}
	current := base
	parts, err := filepath.Rel(base, target)
	if err != nil {
		return "", err
	}
	for _, part := range strings.Split(parts, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			break
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("%s path %q contains symlink %s", label, rel, current)
		}
	}
	return target, nil
}

func ensureResolvedWithin(base, target, label string) (string, error) {
	base, target = filepath.Clean(base), filepath.Clean(target)
	if !pathWithin(base, target) {
		return "", fmt.Errorf("%s path %q escapes the project", label, target)
	}
	ancestor := target
	for {
		_, err := os.Lstat(ancestor)
		if err == nil {
			break
		}
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("checking %s path: %w", label, err)
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return "", fmt.Errorf("cannot resolve %s path %q", label, target)
		}
		ancestor = parent
	}
	realAncestor, err := filepath.EvalSymlinks(ancestor)
	if err != nil {
		return "", fmt.Errorf("resolving %s path: %w", label, err)
	}
	remainder, err := filepath.Rel(ancestor, target)
	if err != nil {
		return "", err
	}
	resolved := filepath.Clean(filepath.Join(realAncestor, remainder))
	if !pathWithin(base, resolved) {
		return "", fmt.Errorf("%s path %q resolves outside the project", label, target)
	}
	return resolved, nil
}

func pathWithin(base, target string) bool {
	rel, err := filepath.Rel(base, target)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func runCopies(dir, rootPath, stage string, cfg *buildManifest, owners map[string]string) error {
	for _, copy := range cfg.Copies {
		src, err := safeOutputPath(dir, copy.From, "copy source")
		if err != nil {
			return err
		}
		finalDst, err := safeOutputPath(dir, copy.To, "copy target")
		if err != nil {
			return err
		}
		dst, err := remapOutput(rootPath, stage, finalDst)
		if err != nil {
			return err
		}
		if !pathWithin(stage, dst) {
			return fmt.Errorf("copy target %q must be inside root", copy.To)
		}
		info, err := os.Stat(src)
		if err != nil {
			return fmt.Errorf("copy %s: %w", copy.From, err)
		}
		if info.IsDir() {
			if err := reserveTreeOutputs(src, dst, owners, "copy "+copy.From); err != nil {
				return err
			}
			if err := copyTree(src, dst); err != nil {
				return fmt.Errorf("copy %s: %w", copy.From, err)
			}
		} else {
			if err := reserveOutput(dst, owners, "copy "+copy.From); err != nil {
				return err
			}
			if err := copyFile(src, dst); err != nil {
				return fmt.Errorf("copy %s: %w", copy.From, err)
			}
		}
	}
	for _, treeCopy := range cfg.TreeCopies {
		base, err := safeOutputPath(dir, treeCopy.From, "treeCopy source")
		if err != nil {
			return err
		}
		for _, include := range treeCopy.Include {
			src, err := safeOutputPath(base, include, "treeCopy include")
			if err != nil {
				return err
			}
			finalDst, err := safeOutputPath(dir, filepath.Join(treeCopy.To, include), "treeCopy target")
			if err != nil {
				return err
			}
			dst, err := remapOutput(rootPath, stage, finalDst)
			if err != nil {
				return err
			}
			if !pathWithin(stage, dst) {
				return fmt.Errorf("treeCopy target %q must be inside root", dst)
			}
			info, err := os.Stat(src)
			if err != nil {
				return fmt.Errorf("treeCopy %s/%s: %w", treeCopy.From, include, err)
			}
			if info.IsDir() {
				if err := reserveTreeOutputs(src, dst, owners, "treeCopy "+treeCopy.From+"/"+include); err != nil {
					return err
				}
				if err := copyTree(src, dst); err != nil {
					return fmt.Errorf("treeCopy %s/%s: %w", treeCopy.From, include, err)
				}
			} else {
				if err := reserveOutput(dst, owners, "treeCopy "+treeCopy.From+"/"+include); err != nil {
					return err
				}
				if err := copyFile(src, dst); err != nil {
					return fmt.Errorf("treeCopy %s/%s: %w", treeCopy.From, include, err)
				}
			}
		}
	}
	return nil
}

func reserveOutput(path string, owners map[string]string, owner string) error {
	key := strings.ToLower(filepath.Clean(path))
	for existing, prior := range owners {
		if existing == key || strings.HasPrefix(existing, key+string(filepath.Separator)) || strings.HasPrefix(key, existing+string(filepath.Separator)) {
			return fmt.Errorf("output collision: %s and %s both write overlapping path %s", prior, owner, path)
		}
	}
	owners[key] = owner
	return nil
}

func reserveTreeOutputs(src, dst string, owners map[string]string, owner string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing to copy symlink %s", path)
		}
		if info.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("refusing to copy non-regular file %s", path)
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		return reserveOutput(filepath.Join(dst, rel), owners, owner)
	})
}

func removeAllRetry(path string) error {
	var err error
	for i := 0; i < 5; i++ {
		err = os.RemoveAll(path)
		if err == nil {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return err
}

func copyFile(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	} else if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to copy symlink %s", src)
	} else if !info.Mode().IsRegular() {
		return fmt.Errorf("refusing to copy non-regular file %s", src)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func copyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing to copy symlink %s", path)
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("refusing to copy non-regular file %s", path)
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(path, target)
	})
}
