//go:build unix

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestCopyRejectsSpecialFilesBeforeBuild(t *testing.T) {
	tests := []struct {
		name   string
		from   string
		inside bool
	}{
		{name: "direct fifo", from: "source.pipe"},
		{name: "fifo in directory", from: "assets", inside: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			mustWrite(t, filepath.Join(dir, "src", "main.js"), "export default 1;\n")
			mustWrite(t, filepath.Join(dir, "dist", "keep.txt"), "last good")
			fifo := filepath.Join(dir, test.from)
			if test.inside {
				fifo = filepath.Join(fifo, "blocked.pipe")
				if err := os.MkdirAll(filepath.Dir(fifo), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if err := unix.Mkfifo(fifo, 0o600); err != nil {
				t.Fatal(err)
			}
			mustWrite(t, filepath.Join(dir, "esgun.config.json"), `{"root":"dist","outdir":"dist/js","entry":["src/main.js"],"copies":[{"from":"`+test.from+`","to":"dist/copied"}]}`)
			err := build(dir, "")
			if err == nil || !strings.Contains(err.Error(), "regular file or directory") && !strings.Contains(err.Error(), "non-regular") {
				t.Fatalf("expected special-file rejection, got %v", err)
			}
			assertFile(t, filepath.Join(dir, "dist", "keep.txt"))
		})
	}
}
