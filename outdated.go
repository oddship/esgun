package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// outdated reports, per declared dependency, the locked version, the highest
// version within the declared range, and the latest stable.
func outdated(dir string) error {
	raw, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return err
	}
	var root pkgJSON
	if err := json.Unmarshal(raw, &root); err != nil {
		return err
	}
	pinned := loadPinned(dir)
	type row struct{ name, current, wanted, latest string }
	var rows []row
	all := map[string]string{}
	for n, r := range root.Dependencies {
		all[n] = r
	}
	for n, r := range root.DevDependencies {
		if _, ok := all[n]; !ok {
			all[n] = r
		}
	}
	for name, rng := range all {
		p, err := fetchPackument(name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  %s: %v\n", name, err)
			continue
		}
		wanted, err := pickVersion(rng, p)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  %s: no version satisfies %q\n", name, rng)
			continue
		}
		rows = append(rows, row{name, pinned[name], wanted.Version, p.DistTags["latest"]})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].name < rows[j].name })
	fmt.Printf("%-30s %-12s %-12s %-12s\n", "package", "current", "wanted", "latest")
	any := false
	for _, r := range rows {
		if r.current != r.wanted {
			any = true
		}
		marker := ""
		if r.current != r.wanted {
			marker = "  <--"
		}
		fmt.Printf("%-30s %-12s %-12s %-12s%s\n", r.name, r.current, r.wanted, r.latest, marker)
	}
	if !any {
		fmt.Println("all dependencies up to date within declared ranges")
	}
	return nil
}
