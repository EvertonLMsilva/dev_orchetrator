//go:build !linux

package composition

import "os"

func acquireInstanceLease(root *os.Root) (func() error, error) {
	// Platforms without a kernel lease retain the existing fail-closed contract.
	if err := root.Mkdir("instance.lock", 0700); err != nil {
		return nil, ErrStorage
	}
	return func() error { return root.Remove("instance.lock") }, nil
}
