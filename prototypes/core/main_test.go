package main

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "fixture-process" {
		fixtureProcess()
		return
	}
	os.Exit(m.Run())
}

func TestPilotChecks(t *testing.T) {
	result, err := selfcheck()
	if err != nil {
		t.Fatal(err)
	}
	if !result.(map[string]any)["passed"].(bool) {
		t.Fatalf("pilot check failed: %#v", result)
	}
}
