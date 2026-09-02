package main

type pkgJSON struct {
	Name            string            `json:"name"`
	Version         string            `json:"version"`
	Dependencies    map[string]string `json:"dependencies,omitempty"`
	DevDependencies map[string]string `json:"devDependencies,omitempty"`
}

// buildManifest describes how to assemble the frontend output. It lives in a
// esgun.config.json next to package.json (the tool-named config-file convention,
// like vite.config / eslint.config), so gun stays generic and each project's
// specifics are data, not code.
type buildManifest struct {
	// Root is removed and recreated at build start (default "dist").
	Root string `json:"root,omitempty"`
	// Outdir is the esbuild output directory (default "dist/js").
	Outdir string `json:"outdir,omitempty"`
	// Entry is the bundle entry set: files or single-level globs.
	Entry []string `json:"entry"`
	// EntrySets and Profiles enable library-style multi-profile builds. They
	// are intentionally separate from Entry so existing manifests retain their
	// historical single minified ESM build.
	EntrySets map[string][]entrySpec  `json:"entrySets,omitempty"`
	Profiles  map[string]buildProfile `json:"profiles,omitempty"`
	// Target is the shared JavaScript language target (default "es2020").
	Target string `json:"target,omitempty"`
	// Resolve adds extra module resolution roots (e.g. vendor/node_modules).
	Resolve []string `json:"resolve,omitempty"`
	// Copies are file-or-directory pairs copied verbatim (from -> to), both
	// relative to the project dir.
	Copies []copySpec `json:"copies,omitempty"`
	// TreeCopies copy a curated subset of a source tree: each entry in Include
	// (a file or directory, relative to From) is copied under To preserving its
	// relative path.
	TreeCopies []treeCopySpec `json:"treeCopies,omitempty"`
	// Watch lists directories to watch in watch mode (default ["src"]).
	Watch []string `json:"watch,omitempty"`
}

// entrySpec maps one input (or glob) to a stable output path. Output omits the
// generated extension and may contain [name] when Input is a glob.
type entrySpec struct {
	Input  string `json:"input"`
	Output string `json:"output"`
}

// buildProfile is one named esbuild invocation. Profiles are executed
// serially so a multi-format library build has a predictable resource ceiling.
type buildProfile struct {
	EntrySets []string `json:"entrySets"`
	Outdir    string   `json:"outdir"`
	Format    string   `json:"format"`
	Splitting bool     `json:"splitting,omitempty"`
	Minify    bool     `json:"minify,omitempty"`
	Suffix    string   `json:"suffix,omitempty"`
	// Target overrides the shared target for this profile.
	Target string `json:"target,omitempty"`
}

type copySpec struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type treeCopySpec struct {
	From    string   `json:"from"`
	To      string   `json:"to"`
	Include []string `json:"include"`
}

type packument struct {
	Name     string                   `json:"name"`
	DistTags map[string]string        `json:"dist-tags"`
	Versions map[string]*packumentVer `json:"versions"`
}

type packumentVer struct {
	Name                 string            `json:"name"`
	Version              string            `json:"version"`
	Dependencies         map[string]string `json:"dependencies"`
	OptionalDependencies map[string]string `json:"optionalDependencies"`
	PeerDependencies     map[string]string `json:"peerDependencies"`
	Dist                 struct {
		Tarball   string `json:"tarball"`
		Integrity string `json:"integrity"`
	} `json:"dist"`
}

// --- lockfile (package-lock.json v3 shape, so Dependabot/npm stay happy) ---
type lockPkg struct {
	Name            string            `json:"name,omitempty"`
	Version         string            `json:"version"`
	Resolved        string            `json:"resolved"`
	Integrity       string            `json:"integrity"`
	Dependencies    map[string]string `json:"dependencies,omitempty"`
	DevDependencies map[string]string `json:"devDependencies,omitempty"`
	Dev             bool              `json:"dev,omitempty"`
}

type lockFile struct {
	Name            string             `json:"name"`
	Version         string             `json:"version"`
	LockfileVersion int                `json:"lockfileVersion"`
	Requires        bool               `json:"requires"`
	Packages        map[string]lockPkg `json:"packages"`
}

// --- resolution (BFS over the dependency graph) ----------------------------
type queued struct {
	name     string
	spec     string
	optional bool
}
