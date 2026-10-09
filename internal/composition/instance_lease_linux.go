package composition

import (
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

const instanceLeaseGuard = ".instance.lease"
const instanceLeaseOwner = "instance.lock/owner"

func privateLeaseFile(root *os.Root, name string, file *os.File) bool {
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return false
	}
	opened, err := file.Stat()
	var stat unix.Stat_t
	return err == nil && os.SameFile(info, opened) && unix.Fstat(int(file.Fd()), &stat) == nil && stat.Uid == uint32(os.Geteuid()) && stat.Nlink == 1
}

func leaseOwnerMatches(root *os.Root, owner string) bool {
	info, err := root.Lstat("instance.lock")
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return false
	}
	file, err := root.OpenFile(instanceLeaseOwner, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return false
	}
	defer file.Close()
	if !privateLeaseFile(root, instanceLeaseOwner, file) {
		return false
	}
	data, err := io.ReadAll(io.LimitReader(file, int64(len(owner)+1)))
	return err == nil && string(data) == owner
}

func acquireInstanceLease(root *os.Root) (func() error, error) {
	// Never unlink this guard: all contenders must lock the same inode, even
	// across crashes. The kernel releases flock only when its owner exits/closes.
	guard, err := root.OpenFile(instanceLeaseGuard, os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0600)
	if err != nil {
		return nil, ErrStorage
	}
	fail := func() (func() error, error) { guard.Close(); return nil, ErrStorage }
	if !privateLeaseFile(root, instanceLeaseGuard, guard) || unix.Flock(int(guard.Fd()), unix.LOCK_EX|unix.LOCK_NB) != nil {
		return fail()
	}
	if !privateLeaseFile(root, instanceLeaseGuard, guard) {
		return fail()
	}
	var stat unix.Stat_t
	if unix.Fstat(int(guard.Fd()), &stat) != nil {
		return fail()
	}
	owner := fmt.Sprintf("kernel-flock-v1:%d:%d\n", stat.Dev, stat.Ino)
	err = root.Mkdir("instance.lock", 0700)
	if err == nil {
		file, createErr := root.OpenFile(instanceLeaseOwner, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if createErr != nil {
			root.Remove("instance.lock")
			return fail()
		}
		_, writeErr := file.WriteString(owner)
		syncErr := file.Sync()
		closeErr := file.Close()
		if writeErr != nil || syncErr != nil || closeErr != nil {
			root.Remove(instanceLeaseOwner)
			root.Remove("instance.lock")
			return fail()
		}
	} else if !errors.Is(err, os.ErrExist) || !leaseOwnerMatches(root, owner) {
		// An empty legacy directory or a different guard identity cannot prove
		// orphanhood. Preserve it rather than guessing from PID/container names.
		return fail()
	}
	return func() error {
		defer guard.Close()
		if !privateLeaseFile(root, instanceLeaseGuard, guard) || !leaseOwnerMatches(root, owner) {
			return ErrStorage
		}
		if root.Remove(instanceLeaseOwner) != nil || root.Remove("instance.lock") != nil {
			return ErrStorage
		}
		return nil
	}, nil
}
