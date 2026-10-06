//go:build linux

package composition

import (
	"errors"
	"io/fs"
	"path/filepath"
	"syscall"
)

func requireReadOnly(path string) error {
	// A read-only parent may contain writable nested mounts. Check every
	// directory and reject symlink/special-file authority before accepting it.
	entries := 0
	return filepath.WalkDir(path, func(current string, d fs.DirEntry, err error) error {
		entries++
		if err != nil || entries > 100000 {
			return errors.New("workspace isolation inspection failed")
		}
		if d.Type()&fs.ModeSymlink != 0 || (!d.IsDir() && !d.Type().IsRegular()) {
			return errors.New("workspace alias or special file denied")
		}
		{
			var stat syscall.Statfs_t
			// ST_RDONLY is bit 0 in Linux statfs flags.
			if syscall.Statfs(current, &stat) != nil || stat.Flags&1 == 0 {
				return errors.New("workspace must be mounted read-only")
			}
		}
		return nil
	})
}
