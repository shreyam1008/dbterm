//go:build !windows

package storeinstall

func Managed() bool { return false }
