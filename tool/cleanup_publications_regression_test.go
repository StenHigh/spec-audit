//go:build darwin || linux

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestPublicationCleanupLargePlanRoundTrip(t *testing.T) {
	f := newExternalFixture(t)
	config := f.install(t)
	completeExternal(t, config, "A")
	backup := exportAuditTestBundle(t, []string{config}, f.base)
	b, err := readBundle(backup)
	if err != nil {
		t.Fatal(err)
	}
	var area BundleArea
	for _, a := range b.Index.Areas {
		if a.Kind == "reports" {
			area = a
		}
	}
	// Planning errors must not be printed as a partial CleanupPlan on stdout.
	value, err := execute([]string{"cleanup", "plan", backup, "--publications", digest([]byte("unknown-publication"))})
	if err == nil || value != nil {
		t.Fatal("planning failure returned an output value", value, err)
	}
	snapshot := PublicationSnapshot{SchemaVersion: 1, Files: []PublicationFile{}}
	objects := []BundleFile{}
	for i := 0; i < 40000; i++ {
		data := []byte(fmt.Sprintf("asset-%05d", i))
		hash := digest(data)
		snapshot.Files = append(snapshot.Files, PublicationFile{Path: "sources/" + hash + ".html", SHA256: hash, Bytes: len(data)})
		objects = append(objects, BundleFile{Area: area.ID, Path: "._objects/" + hash, SHA256: hash, Bytes: int64(len(data)), Mode: 0400})
	}
	snapshot.ID = publicationID(snapshot)
	metadata, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	metadata = append(metadata, '\n')
	if len(metadata) > maxState {
		t.Fatal("reproduction exceeds the existing publication manifest limit")
	}
	basis, err := capturePublicationCleanup(b, []string{snapshot.ID})
	if err != nil {
		t.Fatal(err)
	}
	basis.Files = append(basis.Files, objects...)
	basis.Files = append(basis.Files, BundleFile{Area: area.ID, Path: "._publications/" + snapshot.ID + ".json", SHA256: digest(metadata), Bytes: int64(len(metadata)), Mode: 0600})
	basis.Publications = append(basis.Publications, CleanupPublication{Area: area.ID, Snapshot: snapshot, Manifest: string(metadata)})
	sort.Slice(basis.Files, func(i, j int) bool {
		return basis.Files[i].Area+"/"+basis.Files[i].Path < basis.Files[j].Area+"/"+basis.Files[j].Path
	})
	plan, err := makePublicationCleanupPlan(b, basis)
	if err != nil {
		t.Fatal(err)
	}
	if err := comparePublicationLive(b, plan); err != nil {
		t.Fatal("modeled completed cleanup does not match the live basis", err)
	}

	// Model a supported resumed cleanup: all candidates have already been removed.
	// Retained audit files are real. Small physical deletion prefixes are covered separately.
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(plan); err != nil {
		t.Fatal(err)
	}
	t.Logf("manifest=%d CLI plan=%d candidates=%d", len(metadata), output.Len(), len(plan.Candidates))
	if output.Len() > maxCleanupPlan {
		t.Fatal("supported large plan exceeds its reader limit")
	}
	if output.Len() <= maxState {
		t.Fatal("large regression no longer crosses the former reader limit")
	}
	planPath := filepath.Join(f.base, "large-plan.json")
	writeFixture(t, planPath, output.Bytes())
	before := publicationBytes(t, area.OriginalRoot)
	proof := runOK(t, "cleanup", "verify", backup, planPath).(CleanupProof)
	if proof.PlanID != plan.ID {
		t.Fatal("proof lost exact plan identity")
	}
	proofPath := filepath.Join(f.base, "large-proof.json")
	writeFixture(t, proofPath, mustJSON(t, proof))
	for i := 0; i < 2; i++ {
		result := runOK(t, "cleanup", "apply", backup, planPath, proofPath).(map[string]any)
		if result["complete"] != true || len(result["removed"].([]string)) != 0 || len(result["skipped"].([]string)) != len(plan.Candidates) {
			t.Fatal("large resumed cleanup is not safely repeatable")
		}
	}
	// A sparse oversized input must fail before proof or any deletion.
	oversized := filepath.Join(f.base, "oversized-plan.json")
	file, err := os.Create(oversized)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxCleanupPlan + 1); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"verify", backup, oversized}, {"apply", backup, oversized, proofPath}} {
		if _, err := execute(append([]string{"cleanup"}, args...)); err == nil || !strings.Contains(err.Error(), "лимит") {
			t.Fatal("oversized PLAN did not fail its size bound", err)
		}
	}
	if !reflect.DeepEqual(before, publicationBytes(t, area.OriginalRoot)) {
		t.Fatal("large resumed cleanup changed retained audit")
	}
}

func TestCleanupPlanOutputSizeBoundary(t *testing.T) {
	for _, schema := range []int{1, 2} {
		// Escaping and indentation make the actual CLI output larger than compact JSON.
		p := CleanupPlan{SchemaVersion: schema, ID: "<plan>", Candidates: []CleanupCandidate{{Reason: "<reason>\nline"}}}
		var output bytes.Buffer
		encoder := json.NewEncoder(&output)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(p); err != nil {
			t.Fatal(err)
		}
		if err := checkCleanupPlanSize(p, int64(output.Len())); err != nil {
			t.Fatal("exact CLI boundary rejected", err)
		}
		if err := checkCleanupPlanSize(p, int64(output.Len()-1)); err == nil {
			t.Fatal("trailing LF was not included in limit")
		}
		compact, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		if err := checkCleanupPlanSize(p, int64(len(compact))); err == nil {
			t.Fatal("writer checked compact JSON instead of CLI output")
		}
	}
}
