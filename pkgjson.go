package main

import (
	"bytes"
	"fmt"
	"os"
	"slices"
	"strconv"

	"encoding/json"
	"path/filepath"
)

// loadPackage reads and parses package.json.
func loadPackage(dir string) (*pkgJSON, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return nil, err
	}
	var root pkgJSON
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, err
	}
	return &root, nil
}

// writePackageJSON persists package.json with alphabetically sorted deps.
// writePackageJSON rewrites only the "dependencies" value of package.json,
// preserving every other field and the original top-level key order
// byte-for-byte (so add/remove produce minimal diffs).
func writePackageJSON(dir string, root *pkgJSON) error {
	path := filepath.Join(dir, "package.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return err
	}

	// Pretty-print the (sorted) dependencies maps nested under their keys.
	prettyDepMap := func(m map[string]string) ([]byte, error) {
		if m == nil {
			m = map[string]string{}
		}
		raw, err := json.MarshalIndent(m, "", "  ")
		if err != nil {
			return nil, err
		}
		var nested bytes.Buffer
		if err := json.Indent(&nested, raw, "  ", "  "); err != nil {
			return nil, err
		}
		return nested.Bytes(), nil
	}
	depsRaw, err := prettyDepMap(root.Dependencies)
	if err != nil {
		return err
	}
	m["dependencies"] = depsRaw
	devRaw, err := prettyDepMap(root.DevDependencies)
	if err != nil {
		return err
	}
	m["devDependencies"] = devRaw

	// Preserve the original top-level key order.
	var order []string
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return fmt.Errorf("package.json is not a JSON object")
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		k := tok.(string)
		order = append(order, k)
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return err
		}
	}
	if !slices.Contains(order, "dependencies") {
		order = append(order, "dependencies")
	}
	if !slices.Contains(order, "devDependencies") {
		order = append(order, "devDependencies")
	}

	var out bytes.Buffer
	out.WriteString("{\n")
	for i, k := range order {
		comma := ","
		if i == len(order)-1 {
			comma = ""
		}
		if k == "dependencies" || k == "devDependencies" {
			out.WriteString("  " + strconv.Quote(k) + ": " + string(m[k]) + comma + "\n")
			continue
		}
		var buf bytes.Buffer
		if err := json.Indent(&buf, m[k], "  ", "  "); err != nil {
			return err
		}
		out.WriteString("  " + strconv.Quote(k) + ": " + buf.String() + comma + "\n")
	}
	out.WriteString("}\n")
	return os.WriteFile(path, out.Bytes(), 0o644)
}
