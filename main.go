// esgun: Go-native frontend build tool. Resolves npm dependencies from a
// registry and bundles the frontend with esbuild — no node/npm/bun required
// anywhere.
//
// Project specifics live in package.json (dependencies/devDependencies) and a
// esgun.config.json (see buildManifest) describing entry points and copy
// choreography for `build`.
//
//	esgun deps   [--dir DIR] [--frozen] [-u]   resolve + materialize from lockfile
//	esgun ci     [--dir DIR]                    frozen install (CI)
//	esgun add <pkg>[@range] [--dev] [--dir DIR] add/upgrade a dependency
//	esgun remove <pkg>       [--dir DIR]        remove a dependency
//	esgun update [pkg]       [--dir DIR]        re-resolve within declared ranges
//	esgun outdated           [--dir DIR]        show newer available versions
//	esgun audit              [--dir DIR]        vulnerability report (OSV)
//	esgun build  [--dir DIR]                    bundle entry points with esbuild
//	esgun watch  [--dir DIR]                    rebuild on change (fsnotify + poll)
//
// Flags: --dir DIR, --registry URL, --frozen, --dev, -u
package main

import (
	"fmt"
	"os"
	"strings"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: esgun <deps|ci|install|add|remove|update|outdated|audit|build|watch> [--dir DIR] [--registry URL] [--frozen] [--dev] [-u]")
		os.Exit(2)
	}
	args := os.Args[2:]
	dir := "."
	force := false
	frozen := false
	dev := false
	regURL := ""
	rest := []string{}
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--dir" && i+1 < len(args):
			dir = args[i+1]
			i++
		case args[i] == "--registry" && i+1 < len(args):
			regURL = args[i+1]
			i++
		case args[i] == "-u":
			force = true
		case args[i] == "--frozen":
			frozen = true
		case args[i] == "--dev":
			dev = true
		default:
			rest = append(rest, args[i])
		}
	}
	if regURL != "" {
		registry = strings.TrimRight(regURL, "/")
	} else if env := os.Getenv("ESGUN_REGISTRY"); env != "" {
		registry = strings.TrimRight(env, "/")
	} else {
		registry = defaultRegistry
	}

	var err error
	switch os.Args[1] {
	case "deps", "install":
		err = runDeps(dir, force, nil, frozen)
	case "ci":
		err = runDeps(dir, false, nil, true)
	case "build":
		err = build(dir)
	case "watch":
		err = watch(dir)
	case "add":
		if len(rest) == 0 {
			err = fmt.Errorf("usage: esgun add <pkg>[@range] [--dev] [--dir DIR]")
		} else {
			err = add(dir, rest[0], dev)
		}
	case "remove":
		if len(rest) == 0 {
			err = fmt.Errorf("usage: esgun remove <pkg> [--dir DIR]")
		} else {
			err = remove(dir, rest[0])
		}
	case "update":
		pkg := ""
		if len(rest) > 0 {
			pkg = rest[0]
		}
		err = update(dir, pkg)
	case "outdated":
		err = outdated(dir)
	case "audit":
		found, aerr := audit(dir)
		if aerr != nil {
			fmt.Fprintln(os.Stderr, "error:", aerr)
			os.Exit(1)
		}
		if found {
			os.Exit(1)
		}
		err = nil
	default:
		fmt.Fprintln(os.Stderr, "usage: esgun <deps|ci|install|add|remove|update|outdated|audit|build|watch> [--dir DIR] [--registry URL] [--frozen] [--dev] [-u]")
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
