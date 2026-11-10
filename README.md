# esgun

**esgun** is a Go-native, npm-compatible package manager and esbuild bundler:
it resolves npm dependencies straight from the registry, manages them
(add/remove/update/outdated), audits them against OSV, and bundles your
frontend, all in a single static Go binary. **No node, no npm, no bun.**

```sh
go install github.com/oddship/esgun@latest
cd my-frontend
esgun deps && esgun build
```

`package.json` stays fully standard (`dependencies` + a conventional `scripts`
block); the lockfile is standard `package-lock.json` (lockfileVersion 3, which
npm and Dependabot both accept); the build choreography lives in a tool-named
`esgun.config.json` next to it (the `vite.config` / `eslint.config`
convention).

## Why

The JavaScript toolchain is the odd dependency of most Go web projects: a Go
binary that ships a frontend still needs node/npm/bun installed everywhere it's
built, for what is usually a handful of leaf dependencies copied verbatim into
the binary. esgun replaces that with Go: the registry protocol, semver
resolution, lockfile management, security auditing, and bundling all run as Go
(esbuild as a library), so **Go 1.25.10 or newer is the only toolchain requirement**.

As a deliberate security posture, esgun **never runs lifecycle scripts**:
nothing in the dependency tree ever executes during install, and it verifies
every tarball's SHA-512 against the registry's `dist.integrity` before
anything touches disk.

## Install

Three ways, depending on how you want to consume it:

**1. PATH binary** (for ad-hoc use anywhere):

```sh
go install github.com/oddship/esgun@latest
```

**2. Run without installing:**

```sh
go run github.com/oddship/esgun@latest <cmd>
```

**3. Go tool dependency (recommended for projects):** pin esgun in the
project's `go.mod` so the version is locked with the rest of the toolchain and
contributors get it automatically:

```sh
go get tool github.com/oddship/esgun@latest
```

This adds `tool github.com/oddship/esgun` to `go.mod` (with the resolved
version in `require` + `go.sum`). The project then invokes the pinned build
from anywhere:

```sh
go tool esgun deps
```

Bumping the tool is a normal `go get tool github.com/oddship/esgun@vX.Y.Z`,
same as any other Go dependency. The first `go tool esgun` run compiles it;
subsequent runs use the build cache.

## Commands

```
esgun deps          [--dir DIR] [--frozen] [-u]   # install from lockfile
esgun ci            [--dir DIR]                   # frozen install (fails on lockfile drift)
esgun install       [--dir DIR]                   # alias of deps
esgun add <pkg>[@range] [--dev] [--dir DIR]       # add/upgrade a dependency
esgun remove <pkg>  [--dir DIR]                   # remove a dependency
esgun update [pkg]  [--dir DIR]                   # re-resolve within declared ranges
esgun outdated      [--dir DIR]                   # newer versions available
esgun audit         [--dir DIR]                   # OSV vulnerability report (exit 1 on findings)
esgun build         [--dir DIR] [--profile NAME]  # bundle + assemble the output
esgun watch         [--dir DIR] [--profile NAME]  # rebuild on change

Flags: --dir DIR (default "."), --profile NAME (build/watch only),
--registry URL (or ESGUN_REGISTRY), --frozen, --dev, -u
```

## Getting started

```sh
mkdir my-frontend && cd my-frontend
esgun add dayjs            # writes "dayjs": "^1.11.x" to package.json, installs
esgun add @codemirror/state --dev
esgun build                # needs an esgun.config.json (see below)
```

## deps

- Reads `package.json` as a *manifest* (name/version list only; nothing executes it).
- Resolves the full dependency graph against the registry (abbreviated packuments, exactly like npm itself), honoring npm-style semver ranges (`^`, `~`, `>=`, `||`, …).
- Reuses the existing `package-lock.json` when a pinned version still satisfies its range (installs are reproducible). `-u` re-resolves everything.
- Verifies every tarball's SHA-512 against the registry's `dist.integrity` before extracting.
- Writes a `package-lock.json` (lockfileVersion 3) that npm and Dependabot can consume (`npm ci --dry-run` passes on it).
- Extracts packages into `vendor/node_modules/<name>`; no `node_modules` directory, no lifecycle scripts.

## package management

`add`/`remove`/`update` edit `package.json` (dependency maps stay alphabetically
sorted; every other field is preserved byte-for-byte) and then re-resolve the
graph, rewrite the lockfile, and refresh the vendor tree (stale packages
pruned):

- `esgun add dayjs`            : resolve latest stable (via the registry `dist-tags`),
                               save `^<version>`, install. `esgun add pkg@1.2.3` saves
                               the exact range; `esgun add pkg@~0.7` saves it verbatim;
                               `esgun add pkg@next` resolves a dist-tag.
- `esgun remove dayjs`         : drop from package.json, lockfile, and vendor tree.
- `esgun update`               : re-resolve everything within declared ranges
                               (npm update semantics; package.json unchanged).
- `esgun update dayjs`         : same, for one package.

`devDependencies` are resolved too (marked `"dev": true` in the lockfile);
unsatisfied peer dependencies and unavailable optional dependencies degrade to
warnings instead of errors.

## build

- Bundles the configured `entry` points with esbuild **as a Go library** (`github.com/evanw/esbuild`): ESM output, code splitting into `chunks/[name]-[hash].js`, minified, browser target. Package imports resolve through the `resolve` dirs (e.g. `vendor/node_modules`) via esbuild `NodePaths`.
- The assembly choreography is **project data, not tool code**: an `esgun.config.json` next to `package.json` declares the output root, entry set (explicit paths or `*` globs), extra resolve roots, verbatim file/directory copies, curated tree copies, and watch dirs.

```jsonc
// esgun.config.json
{
  "root": "dist",
  "target": "esnext",
  "outdir": "dist/js",
  "entry": ["src/main.js", "src/views/*.js"],
  "resolve": ["vendor/node_modules"],
  "copies": [
    { "from": "src/static", "to": "dist" },
    { "from": "vendor/node_modules/chart.js/dist/chart.umd.js", "to": "dist/vendor/chart.min.js" }
  ],
  "treeCopies": [
    { "from": "vendor/node_modules/tinymce", "to": "dist/tinymce",
      "include": ["tinymce.min.js", "plugins/image/plugin.min.js"] }
  ]
}
```

`package.json` stays fully standard: deps plus a conventional `scripts` block
(`"build": "esgun build"`, `"watch": "esgun watch"`).

### Named build profiles

Libraries that publish several formats can replace the legacy `entry` and
`outdir` fields with named entry sets and profiles. Legacy manifests keep their
original minified, split ESM behavior unchanged.

```json
{
  "root": "dist",
  "resolve": ["vendor/node_modules"],
  "entrySets": {
    "library": [
      { "input": "src/index.js", "output": "library" }
    ],
    "components": [
      { "input": "src/components/*.js", "output": "components/[name]" },
      { "input": "src/components/*.css", "output": "components/[name]" }
    ]
  },
  "profiles": {
    "esm-readable": {
      "entrySets": ["library", "components"],
      "outdir": "dist",
      "format": "esm",
      "suffix": ".esm"
    },
    "iife-readable": {
      "entrySets": ["library"],
      "outdir": "dist",
      "format": "iife"
    },
    "iife-minified": {
      "entrySets": ["library"],
      "outdir": "dist",
      "format": "iife",
      "minify": true,
      "suffix": ".min"
    }
  },
  "copies": [
    { "from": "src/themes", "to": "dist/themes" }
  ]
}
```

Entry `output` values are stable paths relative to a profile's `outdir` and
omit the generated extension. A glob input must use exactly one `[name]`
placeholder. `suffix` is inserted before both `.js` and `.css`, so `library`
with `"suffix": ".min"` becomes `library.min.js`.

`format` is either `esm` or `iife`; `minify` and `splitting` default to false.
IIFE profiles cannot use splitting. Self-contained component entry points
should also leave splitting disabled. `target` defaults to the legacy `es2020`
behavior and may be set to `esnext` globally or overridden by one profile when
a library should preserve modern syntax for smaller published artifacts.

`esgun build` executes every profile serially in lexical name order. Use
`esgun build --profile esm-readable` to build only one. A selected build still
replaces the complete `root` and runs the shared copies once, so it never leaves
stale outputs from other profiles. Builds are assembled in a sibling staging
directory and replace the previous output only after every bundle and copy
succeeds. Output aliases and copy destinations are collision checked.

### Output directories mounted as volumes

By default esgun publishes a completed stage by renaming the output root. If
`root` is a Docker volume or another mount point, enable `preserveRoot`:

```json
{
  "root": "dist",
  "preserveRoot": true,
  "entry": ["src/main.js"]
}
```

This preserves the root directory's identity, replaces its contents, and removes
stale files. Legacy and profile builds (including watch rebuilds) use the same
publishing mode. Compilation and copy failures leave the live output untouched.
Existing files are backed up before publication; publication errors restore them.
If restoration also fails, the error reports the retained backup directory for
manual recovery. Symlinks and special files in staged or previous output are
rejected before publication.

This mode copies files so it also works across a mount's filesystem boundary.
It needs space for a backup and does not provide an atomic directory swap: readers
can see a partially populated directory during publication. The default remains
the rename-based publisher; `preserveRoot` is opt-in.

## watch

Rebuilds on any change under the configured `watch` dirs (default `src`,
100 ms debounce). Uses inotify via [fsnotify](https://github.com/fsnotify/fsnotify)
plus a 300 ms mtime poll as a fallback, because host-mounted workspaces may not
deliver inotify events for host-side editor writes. `--profile NAME` applies the
same selection as `build`. Changes during a build coalesce into at most one
follow-up build.

## audit

`esgun audit` queries the free [OSV](https://osv.dev) database for every
resolved version in the lockfile and exits 1 if any vulnerabilities are found;
drop-in `npm audit` behavior for CI, without a node runtime.

## Status

MVP: single-package layout, flat resolution (no workspaces/monorepos),
no lifecycle scripts, no private-registry auth, no publish command. All by design.
Known follow-ups: unit tests for the resolver and review of semver edge cases beyond
[Masterminds/semver](https://github.com/Masterminds/semver)'s coverage.
