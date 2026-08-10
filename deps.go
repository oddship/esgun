package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"

	"encoding/json"
	"path/filepath"
)

// --- commands --------------------------------------------------------------
// runDeps resolves the dependency graph, writes the lockfile, and materializes
// the vendor tree. forceAll re-resolves everything; forceNames re-resolves
// only the named packages (used by add/update); frozen fails instead of
// re-resolving when the lockfile is out of sync (npm ci).
func runDeps(dir string, forceAll bool, forceNames map[string]bool, frozen bool) error {
	raw, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return err
	}
	var root pkgJSON
	if err := json.Unmarshal(raw, &root); err != nil {
		return err
	}
	pinned := loadPinned(dir)
	if frozen {
		fmt.Printf("resolving %d direct deps of %s (frozen)\n", len(root.Dependencies), root.Name)
	} else if forceAll || len(forceNames) > 0 {
		fmt.Printf("resolving %d direct deps of %s (fresh)\n", len(root.Dependencies), root.Name)
	} else if len(pinned) > 0 {
		fmt.Printf("resolving %d direct deps of %s (lockfile-pinned)\n", len(root.Dependencies), root.Name)
	} else {
		fmt.Printf("resolving %d direct deps of %s\n", len(root.Dependencies), root.Name)
	}
	resolved, err := resolve(root.Dependencies, root.DevDependencies, pinned, forceAll, forceNames, frozen)
	if err != nil {
		return err
	}

	// Report what changed relative to the previous lockfile.
	changed := false
	for name, v := range resolved {
		if old, ok := pinned[name]; ok {
			if old != v.Version {
				fmt.Printf("  updated %s: %s -> %s\n", name, old, v.Version)
				changed = true
			}
		} else {
			fmt.Printf("  added %s@%s\n", name, v.Version)
			changed = true
		}
	}
	for name := range pinned {
		if _, ok := resolved[name]; !ok {
			fmt.Printf("  removed %s\n", name)
			changed = true
		}
	}
	if !changed {
		fmt.Println("  no changes")
	}

	devSet := devReachable(&root, resolved)
	if err := writeLockfile(dir, &root, resolved, devSet); err != nil {
		return err
	}
	fmt.Printf("lockfile: %s\n", filepath.Join(dir, "package-lock.json"))

	vendor := filepath.Join(dir, "vendor", "node_modules")
	names := make([]string, 0, len(resolved))
	for n := range resolved {
		names = append(names, n)
	}
	sort.Strings(names)

	// Parallel materialization with vendor reuse: packages already extracted at
	// the resolved version are kept (their own package.json is the version
	// marker); everything else is downloaded, integrity-verified, and extracted
	// concurrently (6 workers). Optional packages degrade to warnings.
	optSet := optionalSet(resolved)
	var wg sync.WaitGroup
	sem := make(chan struct{}, 6)
	errCh := make(chan error, len(names))
	for _, name := range names {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			v := resolved[name]
			dest := filepath.Join(vendor, filepath.FromSlash(name))
			if hasVersion(dest, v.Version) {
				fmt.Printf("  cached %s@%s\n", name, v.Version)
				return
			}
			body, err := fetchVerified(v)
			if err != nil {
				if optSet[name] {
					fmt.Fprintf(os.Stderr, "  warning: optional %s skipped: %v\n", name, err)
					return
				}
				errCh <- fmt.Errorf("%s: %w", name, err)
				return
			}
			if err := extractTar(body, dest); err != nil {
				if optSet[name] {
					fmt.Fprintf(os.Stderr, "  warning: optional %s skipped: %v\n", name, err)
					return
				}
				errCh <- fmt.Errorf("%s: %w", name, err)
				return
			}
			fmt.Printf("  extracted %s@%s (%d KB)\n", name, v.Version, len(body)/1024)
		}(name)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		return err
	}
	if err := pruneVendor(vendor, resolved); err != nil {
		return err
	}
	fmt.Printf("done: %d packages in %s\n", len(resolved), vendor)
	return nil
}

// parseSpec splits "name@range" (scoped names handled: split at the last @).
func parseSpec(spec string) (name, rng string, err error) {
	if i := strings.LastIndex(spec, "@"); i > 0 {
		return spec[:i], spec[i+1:], nil
	}
	return spec, "", nil
}

// add adds or upgrades a dependency: updates package.json, then re-resolves.
// With no range it behaves like npm install <pkg>: save "^<latest>".
// add adds or upgrades a dependency: updates package.json, then re-resolves.
// No range means "latest stable" (saved as ^<version>); a registry dist-tag
// (e.g. @next, @beta) resolves through the packument's dist-tags. --dev puts
// the dep in devDependencies.
func add(dir, spec string, dev bool) error {
	name, rng, err := parseSpec(spec)
	if err != nil {
		return err
	}
	p, err := fetchPackument(name)
	if err != nil {
		return err
	}
	var v *packumentVer
	switch {
	case rng == "":
		v, err = pickVersion("*", p)
		if err != nil {
			return err
		}
		rng = "^" + v.Version
	case p.DistTags != nil && p.DistTags[rng] != "":
		// A registry dist-tag, not a semver range (e.g. pkg@next).
		if tv, ok := p.Versions[p.DistTags[rng]]; ok {
			v = tv
			rng = "^" + v.Version
		} else {
			return fmt.Errorf("dist-tag %q of %q points at missing version", rng, name)
		}
	default:
		v, err = pickVersion(rng, p)
		if err != nil {
			return fmt.Errorf("no version of %q satisfies %q", name, rng)
		}
	}

	raw, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return err
	}
	var root pkgJSON
	if err := json.Unmarshal(raw, &root); err != nil {
		return err
	}
	section := &root.Dependencies
	sectionName := "dependencies"
	if dev {
		section = &root.DevDependencies
		sectionName = "devDependencies"
	}
	if *section == nil {
		*section = map[string]string{}
	}
	if old, ok := (*section)[name]; ok {
		fmt.Printf("%s: %s -> %s (%s)\n", name, old, rng, sectionName)
	} else {
		fmt.Printf("%s@%s added to %s\n", name, rng, sectionName)
	}
	(*section)[name] = rng
	if err := writePackageJSON(dir, &root); err != nil {
		return err
	}
	return runDeps(dir, false, map[string]bool{name: true}, false)
}

// remove drops a dependency from package.json and prunes its tree.
// remove drops a dependency from package.json (prod or dev) and prunes its tree.
func remove(dir, name string) error {
	raw, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return err
	}
	var root pkgJSON
	if err := json.Unmarshal(raw, &root); err != nil {
		return err
	}
	section := ""
	if _, ok := root.Dependencies[name]; ok {
		delete(root.Dependencies, name)
		section = "dependencies"
	} else if _, ok := root.DevDependencies[name]; ok {
		delete(root.DevDependencies, name)
		section = "devDependencies"
	} else {
		return fmt.Errorf("%q is not in dependencies or devDependencies", name)
	}
	fmt.Printf("removed %s from %s\n", name, section)
	if err := writePackageJSON(dir, &root); err != nil {
		return err
	}
	return runDeps(dir, false, nil, false)
}

// update re-resolves dependencies within their declared ranges: all packages,
// or a single named package (npm update semantics; package.json unchanged).
// update re-resolves dependencies within their declared ranges: all packages,
// or a single named package (npm update semantics; package.json unchanged).
func update(dir, pkg string) error {
	if pkg == "" {
		fmt.Println("re-resolving all deps within declared ranges")
		return runDeps(dir, true, nil, false)
	}
	return runDeps(dir, false, map[string]bool{pkg: true}, false)
}
