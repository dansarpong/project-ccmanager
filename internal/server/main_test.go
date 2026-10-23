package server

import (
	"fmt"
	"os"
	"testing"
)

// TestMain points managedPolicyDir at an empty directory, so a managed policy
// installed on the machine running the tests never shows up in them.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "ccmanager-managed-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	managedPolicyDir = dir
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
