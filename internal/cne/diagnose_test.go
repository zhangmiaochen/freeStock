//go:build windows

package cne

import "testing"

func TestDiagnoseReport(t *testing.T) {
	t.Log("\n" + Diagnose())
}
