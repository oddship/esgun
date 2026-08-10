package main

import (
	"fmt"
	"os"
	"strings"

	"encoding/json"
	"path/filepath"
)

func writeLockfile(dir string, root *pkgJSON, resolved map[string]*packumentVer, devSet map[string]bool) error {
	pkgs := map[string]lockPkg{}
	rootDeps := lockPkg{Name: root.Name, Version: root.Version, Dependencies: root.Dependencies, DevDependencies: root.DevDependencies}
	pkgs[""] = rootDeps
	for name, v := range resolved {
		p := lockPkg{
			Version:      v.Version,
			Resolved:     v.Dist.Tarball,
			Integrity:    v.Dist.Integrity,
			Dependencies: v.Dependencies,
		}
		if devSet[name] {
			p.Dev = true
		}
		pkgs["node_modules/"+name] = p
	}
	lf := lockFile{
		Name:            root.Name,
		Version:         root.Version,
		LockfileVersion: 3,
		Requires:        true,
		Packages:        pkgs,
	}
	raw, err := json.MarshalIndent(lf, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "package-lock.json"), raw, 0o644)
}

// loadPinned reads the previous lockfile's resolved versions, keyed by package
// name. Returns nil if there is no lockfile.
func loadLockfile(dir string) (*lockFile, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "package-lock.json"))
	if err != nil {
		return nil, err
	}
	var lf lockFile
	if err := json.Unmarshal(raw, &lf); err != nil {
		return nil, fmt.Errorf("parsing package-lock.json: %w", err)
	}
	return &lf, nil
}

func loadPinned(dir string) map[string]string {
	lf, err := loadLockfile(dir)
	if err != nil {
		return nil
	}
	pinned := map[string]string{}
	for path, p := range lf.Packages {
		if strings.HasPrefix(path, "node_modules/") {
			pinned[strings.TrimPrefix(path, "node_modules/")] = p.Version
		}
	}
	return pinned
}
