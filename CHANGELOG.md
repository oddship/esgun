# Changelog

## [0.1.0] - 2026-11-10

First tagged release of esgun, a Go-native npm-compatible dependency manager and
esbuild frontend bundler.

### Added

- npm dependency resolution, add/remove/update/outdated commands, frozen installs,
  standard package-lock.json v3 output, and SHA-512 tarball verification.
- OSV dependency auditing without a JavaScript runtime.
- Legacy minified ESM builds and named ESM/IIFE profiles with output collision
  checks, staged publication, static copies, and curated tree copies.
- Optional gzip output with extension and level selection, deterministic headers,
  preserved file permissions, optional original removal, and collision checks (#2).
- Opt-in `preserveRoot` publishing for mounted output directories, including
  cross-filesystem copies, stale-file cleanup, rollback, and retained backups when
  rollback fails (#3).
- Prebuilt Linux, macOS, and Windows binaries for amd64 and arm64, with checksums.

### Fixed

- Watch mode rebuilds after config-only edits and atomic manifest saves, reloads
  watch paths safely, and recovers after invalid edits. Rebuilds are serialized (#1).
- Publication copies keep private directories private while files are copied,
  as well as restoring their final permissions.
- The documented Go tool installation command now uses `go get -tool` so the
  module is actually registered as a project tool.

### Compatibility

- Go 1.25.10 or newer is required to build from source. Node, npm, and Bun are not
  needed for dependency installation or bundling. Lifecycle scripts never run.
- Gzip and mounted-root publishing are opt-in. Mounted-root publication requires
  backup space and is not an atomic directory swap; readers can see partial
  output during publication.
- The dependency layout remains flat and single-package. Workspaces, private
  registry authentication, and package publishing are not supported.

[0.1.0]: https://github.com/oddship/esgun/releases/tag/v0.1.0
