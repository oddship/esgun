package main

import (
	"encoding/json"
	"fmt"
	"github.com/Masterminds/semver/v3"
	"net/http"
	"strings"
)

var registry = "https://registry.npmjs.org"

// --- manifests -------------------------------------------------------------
const defaultRegistry = "https://registry.npmjs.org"

// --- registry client -------------------------------------------------------
var httpClient = &http.Client{}

func fetchPackument(name string) (*packument, error) {
	url := registry + "/" + strings.ReplaceAll(name, "/", "%2F")
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	// Abbreviated ("corgi") packument — exactly what npm itself requests.
	req.Header.Set("Accept", "application/vnd.npm.install-v1+json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch %s: HTTP %d", url, resp.StatusCode)
	}
	var p packument
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		return nil, fmt.Errorf("decode %s: %w", url, err)
	}
	return &p, nil
}

// pickVersion returns the highest version satisfying an npm-style range.
func pickVersion(spec string, p *packument) (*packumentVer, error) {
	versions := p.Versions
	if spec == "" || spec == "*" || spec == "latest" {
		// npm resolves bare "latest" via the registry's dist-tag, which points at
		// the latest stable release (not a newer prerelease like 2.0.0-alpha).
		if lt, ok := p.DistTags["latest"]; ok {
			if v, ok2 := versions[lt]; ok2 {
				return v, nil
			}
		}
		// Fallback: highest stable, then highest prerelease.
		var best, bestPre *semver.Version
		var bestV, bestPreV *packumentVer
		for vs := range versions {
			v, err := semver.NewVersion(vs)
			if err != nil {
				continue
			}
			if v.Prerelease() != "" {
				if bestPre == nil || v.GreaterThan(bestPre) {
					bestPre, bestPreV = v, versions[vs]
				}
				continue
			}
			if best == nil || v.GreaterThan(best) {
				best, bestV = v, versions[vs]
			}
		}
		if bestV != nil {
			return bestV, nil
		}
		if bestPreV != nil {
			return bestPreV, nil
		}
		return nil, fmt.Errorf("no version of %q found", p.Name)
	}

	c, err := semver.NewConstraint(spec)
	if err != nil {
		return nil, fmt.Errorf("bad range %q: %w", spec, err)
	}
	var best *semver.Version
	var bestV *packumentVer
	for vs := range versions {
		v, err := semver.NewVersion(vs)
		if err != nil {
			continue // e.g. dist-tag junk keys
		}
		if !c.Check(v) {
			continue
		}
		if best == nil || v.GreaterThan(best) {
			best, bestV = v, versions[vs]
		}
	}
	if bestV == nil {
		return nil, fmt.Errorf("no version of %q satisfies %q", p.Name, spec)
	}
	return bestV, nil
}

func firstKey(m map[string]*packumentVer) string {
	for k := range m {
		return k
	}
	return ""
}
