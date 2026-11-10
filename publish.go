package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

func publishOutput(root, stage string, preserveRoot bool) error {
	if !preserveRoot {
		return replaceOutputRoot(root, stage)
	}
	return publishRootContents(root, stage, copyPublicationTree)
}

// publishRootContents deliberately avoids renaming root or moving children
// across filesystems. This supports named volumes whose root is a mount point.
// Bundles and copies must already have succeeded in staging before calling it.
func publishRootContents(root, stage string, copyOutput func(string, string) error) error {
	if err := validatePublicationTree(stage); err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	if err := validatePublicationTree(root); err != nil {
		return err
	}
	backup, err := os.MkdirTemp(filepath.Dir(root), "."+filepath.Base(root)+".backup-")
	if err != nil {
		return err
	}
	keepBackup := false
	defer func() {
		if !keepBackup {
			os.RemoveAll(backup)
		}
	}()
	// Complete the backup before touching live files. Copying, rather than
	// renaming, works even when the mounted root and its parent have different
	// devices. Backups stay private (MkdirTemp's 0700 permissions).
	if err := copyOutput(root, backup); err != nil {
		return fmt.Errorf("backing up output contents: %w", err)
	}
	publishErr := clearOutputContents(root)
	if publishErr == nil {
		publishErr = copyOutput(stage, root)
	}
	if publishErr != nil {
		rollbackErr := clearOutputContents(root)
		if rollbackErr == nil {
			rollbackErr = copyOutput(backup, root)
		}
		if rollbackErr != nil {
			keepBackup = true
			return fmt.Errorf("publishing output failed and restoring %s also failed; previous output retained at %s: %w", root, backup, errors.Join(publishErr, rollbackErr))
		}
		return fmt.Errorf("publishing output failed; restored previous contents: %w", publishErr)
	}
	if err := os.RemoveAll(backup); err != nil {
		keepBackup = true
		return fmt.Errorf("removing output backup %s: %w", backup, err)
	}
	return nil
}

func validatePublicationTree(root string) error {
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing to publish symlink %s", path)
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("refusing to publish non-regular file %s", path)
		}
		return nil
	})
}

func clearOutputContents(root string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

// Preserve nested directory permissions in backups and restores, without
// changing the mounted root's own permissions or the private backup root.
func copyPublicationTree(src, dst string) error {
	return copyPublicationFiles(src, dst, copyFile)
}

func copyPublicationFiles(src, dst string, copyFile func(string, string) error) error {
	type directory struct {
		path string
		mode os.FileMode
	}
	var dirs []directory
	if err := filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing to publish symlink %s", path)
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("refusing to publish non-regular file %s", path)
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if !info.IsDir() {
			return copyFile(path, target)
		}
		if rel == "." {
			return os.MkdirAll(target, 0755)
		}
		// Permit the owner to populate read-only source directories without ever
		// giving group/other users more access than the source directory allowed.
		if err := os.MkdirAll(target, info.Mode().Perm()|0700); err != nil {
			return err
		}
		dirs = append(dirs, directory{target, info.Mode().Perm()})
		return nil
	}); err != nil {
		return err
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := os.Chmod(dirs[i].path, dirs[i].mode); err != nil {
			return err
		}
	}
	return nil
}
