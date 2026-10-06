//go:build !linux

package composition

import "errors"

func requireReadOnly(string) error { return errors.New("read-only service requires Linux") }
