package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
)

// --- vulnerability audit (OSV) ---------------------------------------------
type osvResponse struct {
	Vulns []struct {
		ID      string `json:"id"`
		Summary string `json:"summary"`
	} `json:"vulns"`
}

// audit queries the OSV database for the lockfile's resolved versions and
// returns whether any vulnerabilities were found.
func audit(dir string) (bool, error) {
	lf, err := loadLockfile(dir)
	if err != nil {
		return false, err
	}
	var names []string
	for path := range lf.Packages {
		if strings.HasPrefix(path, "node_modules/") {
			names = append(names, strings.TrimPrefix(path, "node_modules/"))
		}
	}
	sort.Strings(names)
	fmt.Printf("auditing %d packages against OSV...\n", len(names))

	type result struct {
		name string
		ver  string
		id   string
		sum  string
	}
	var mu sync.Mutex
	var results []result
	var wg sync.WaitGroup
	sem := make(chan struct{}, 6)
	for _, name := range names {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			p := lf.Packages["node_modules/"+name]
			q := struct {
				Package struct {
					Name      string `json:"name"`
					Ecosystem string `json:"ecosystem"`
				} `json:"package"`
				Version string `json:"version"`
			}{}
			q.Package.Name = name
			q.Package.Ecosystem = "npm"
			q.Version = p.Version
			qBody, err := json.Marshal(q)
			if err != nil {
				return
			}
			resp, err := httpClient.Post("https://api.osv.dev/v1/query", "application/json", bytes.NewReader(qBody))
			if err != nil {
				fmt.Fprintf(os.Stderr, "  %s: osv query failed: %v\n", name, err)
				return
			}
			defer resp.Body.Close()
			var o osvResponse
			if err := json.NewDecoder(resp.Body).Decode(&o); err != nil {
				return
			}
			for _, v := range o.Vulns {
				mu.Lock()
				results = append(results, result{name, p.Version, v.ID, v.Summary})
				mu.Unlock()
			}
		}(name)
	}
	wg.Wait()

	if len(results) == 0 {
		fmt.Println("no known vulnerabilities")
		return false, nil
	}
	fmt.Printf("%d vulnerability(ies) found:\n", len(results))
	for _, r := range results {
		fmt.Printf("  %s@%s [%s] %s\n", r.name, r.ver, r.id, r.sum)
	}
	return true, nil
}
