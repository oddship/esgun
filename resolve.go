package main

import (
	"fmt"
	"github.com/Masterminds/semver/v3"
	"os"
	"sync"
)

// resolve walks the dependency graph. When pinned has a version for a package
// that still satisfies the requested range, it is reused (lockfile-driven,
// npm-like). forceAll bypasses the lockfile for everything; forceNames for the
// named packages only. frozen fails instead of re-resolving when a lockfile
// pin is missing or no longer satisfies a range (npm ci semantics).
//
// The graph is the union of prod (dependencies) and dev (devDependencies)
// roots. Optional dependencies are tolerated: resolution or fetch failures
// degrade to a warning instead of an error. Peer dependencies that cannot be
// satisfied are reported as warnings (gun does not auto-install peers).
func resolve(deps, devDeps map[string]string, pinned map[string]string, forceAll bool, forceNames map[string]bool, frozen bool) (map[string]*packumentVer, error) {
	out := map[string]*packumentVer{}
	queue := []queued{}
	for n, r := range deps {
		queue = append(queue, queued{n, r, false})
	}
	for n, r := range devDeps {
		if _, ok := deps[n]; ok {
			continue // prod wins
		}
		queue = append(queue, queued{n, r, false})
	}
	seen := map[string]bool{}
	var warn func(string)
	warn = func(msg string) { fmt.Fprintf(os.Stderr, "  warning: %s\n", msg) }

	// Batch the BFS: fetch packuments for a batch of queued names concurrently,
	// then resolve versions and enqueue newly discovered dependencies.
	const fetchWorkers = 16
	for len(queue) > 0 {
		n := min(fetchWorkers, len(queue))
		batch := queue[:n]
		queue = queue[n:]
		type fetched struct {
			d queued
			p *packument
			e error
		}
		results := make([]fetched, n)
		var wg sync.WaitGroup
		for i, d := range batch {
			if seen[d.name] {
				continue
			}
			wg.Add(1)
			go func(i int, d queued) {
				defer wg.Done()
				p, err := fetchPackument(d.name)
				results[i] = fetched{d, p, err}
			}(i, d)
		}
		wg.Wait()
		for _, r := range results {
			if r.p == nil && r.e == nil {
				continue // duplicate in batch, already handled
			}
			d := r.d
			if seen[d.name] {
				continue
			}
			seen[d.name] = true
			if r.e != nil {
				if d.optional {
					warn(fmt.Sprintf("optional dep %s unavailable: %v", d.name, r.e))
					continue
				}
				return nil, r.e
			}
			p := r.p
			var err error
			var v *packumentVer
			if !forceAll && !forceNames[d.name] {
				if pin, ok := pinned[d.name]; ok {
					if pv, ok2 := p.Versions[pin]; ok2 && specSatisfies(d.spec, pin) {
						v = pv
					}
				}
			}
			if v == nil {
				if frozen && !forceAll && !forceNames[d.name] {
					return nil, fmt.Errorf("lockfile out of sync: %s@%s has no pinned version; run 'esgun deps'", d.name, d.spec)
				}
				v, err = pickVersion(d.spec, p)
				if err != nil {
					if d.optional {
						warn(fmt.Sprintf("optional dep %s@%s unresolvable", d.name, d.spec))
						continue
					}
					return nil, err
				}
			}
			out[d.name] = v
			for dep, rng := range v.Dependencies {
				if !seen[dep] {
					queue = append(queue, queued{dep, rng, false})
				}
			}
			for dep, rng := range v.OptionalDependencies {
				if !seen[dep] {
					queue = append(queue, queued{dep, rng, true})
				}
			}
			fmt.Printf("  %s@%s\n", d.name, v.Version)
		}
	}

	// Peer dependency warnings (post-resolution, so the graph is complete).
	for name, v := range out {
		for peer, rng := range v.PeerDependencies {
			if pv, ok := out[peer]; ok {
				if !specSatisfies(rng, pv.Version) {
					warn(fmt.Sprintf("%s peer %s@%s not satisfied by %s", name, peer, rng, pv.Version))
				}
			} else {
				warn(fmt.Sprintf("%s peer %s@%s is not installed", name, peer, rng))
			}
		}
	}
	return out, nil
}

// specSatisfies reports whether a version satisfies an npm-style range.
func specSatisfies(spec, version string) bool {
	if spec == "" || spec == "*" || spec == "latest" {
		return true
	}
	c, err := semver.NewConstraint(spec)
	if err != nil {
		return false
	}
	v, err := semver.NewVersion(version)
	if err != nil {
		return false
	}
	return c.Check(v)
}

// --- lockfile --------------------------------------------------------------
// devReachable returns the set of package names only reachable through
// devDependencies (npm's "dev": true marking).
func devReachable(root *pkgJSON, resolved map[string]*packumentVer) map[string]bool {
	prod := map[string]bool{}
	queue := make([]string, 0, len(root.Dependencies))
	for n := range root.Dependencies {
		queue = append(queue, n)
	}
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		if prod[n] {
			continue
		}
		prod[n] = true
		if v, ok := resolved[n]; ok {
			for dep := range v.Dependencies {
				if !prod[dep] {
					queue = append(queue, dep)
				}
			}
		}
	}
	dev := map[string]bool{}
	queue = make([]string, 0, len(root.DevDependencies))
	for n := range root.DevDependencies {
		queue = append(queue, n)
	}
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		if dev[n] {
			continue
		}
		dev[n] = true
		if v, ok := resolved[n]; ok {
			for dep := range v.Dependencies {
				if !dev[dep] {
					queue = append(queue, dep)
				}
			}
		}
	}
	out := map[string]bool{}
	for n := range dev {
		if !prod[n] {
			out[n] = true
		}
	}
	return out
}
