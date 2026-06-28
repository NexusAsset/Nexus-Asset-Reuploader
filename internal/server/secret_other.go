//go:build !windows

package server

func secretSeal(in []byte) []byte          { return in }
func secretOpen(in []byte) ([]byte, bool)  { return in, true }
