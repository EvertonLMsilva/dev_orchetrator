//go:build !linux

package composition

func privateSecurityFile(string) error { return ErrConfig }
