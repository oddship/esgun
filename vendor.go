package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"

	"archive/tar"
	"compress/gzip"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"path/filepath"
)

// --- download + integrity + extract ---------------------------------------
func fetchVerified(v *packumentVer) ([]byte, error) {
	resp, err := httpClient.Get(v.Dist.Tarball)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", v.Dist.Tarball, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: HTTP %d", v.Dist.Tarball, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if v.Dist.Integrity != "" {
		sum := sha512.Sum512(body)
		got := "sha512-" + base64.StdEncoding.EncodeToString(sum[:])
		if got != v.Dist.Integrity {
			return nil, fmt.Errorf("integrity mismatch for %s@%s", v.Name, v.Version)
		}
	}
	return body, nil
}

func extractTar(body []byte, dest string) error {
	gz, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	cleanDest := filepath.Clean(dest)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		// npm tarballs carry a single "package/" root dir.
		name := strings.TrimPrefix(hdr.Name, "package/")
		if name == "" {
			continue
		}
		target := filepath.Join(dest, filepath.FromSlash(name))
		if !strings.HasPrefix(filepath.Clean(target), cleanDest+string(os.PathSeparator)) {
			return fmt.Errorf("path escape from tarball: %s", hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			f, err := os.Create(target)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			f.Close()
		case tar.TypeSymlink, tar.TypeLink:
			// not needed for these packages; skip
		}
	}
	return nil
}

// hasVersion reports whether the extracted package at dest matches the
// resolved version (its own package.json is the version marker).
func hasVersion(dest, version string) bool {
	raw, err := os.ReadFile(filepath.Join(dest, "package.json"))
	if err != nil {
		return false
	}
	var p struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return false
	}
	return p.Version == version
}

// optionalSet returns the names declared as optionalDependencies anywhere in
// the resolved graph (fetch/extract failures for these degrade to warnings).
func optionalSet(resolved map[string]*packumentVer) map[string]bool {
	out := map[string]bool{}
	for _, v := range resolved {
		for d := range v.OptionalDependencies {
			out[d] = true
		}
	}
	return out
}

// pruneVendor removes package trees under vendor/node_modules that are not in
// the resolved set (handles uninstalls and stale transitive deps).
func pruneVendor(vendor string, resolved map[string]*packumentVer) error {
	entries, err := os.ReadDir(vendor)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	prune := func(p string) {
		fmt.Printf("  pruned %s\n", filepath.Base(p))
		removeAllRetry(p)
	}
	for _, e := range entries {
		name := e.Name()
		p := filepath.Join(vendor, name)
		if strings.HasPrefix(name, "@") {
			// Scoped: prune any sub-package not in the resolved set.
			subs, err := os.ReadDir(p)
			if err != nil {
				continue
			}
			for _, s := range subs {
				full := name + "/" + s.Name()
				if _, ok := resolved[full]; !ok {
					prune(filepath.Join(p, s.Name()))
				}
			}
			if left, _ := os.ReadDir(p); len(left) == 0 {
				prune(p)
			}
		} else if _, ok := resolved[name]; !ok {
			prune(p)
		}
	}
	return nil
}
