//go:build !windows

package cne

func processSessionID() uint32 { return 0 }
func isElevated() bool         { return false }
func currentPPID() int         { return 0 }
