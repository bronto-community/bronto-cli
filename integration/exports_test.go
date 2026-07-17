package integration

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// TestExports_CreateWaitDownload exercises `exports create` with the
// convenience flags (--dataset/--since/--where) plus --wait --download:
// create against the seeded dataset/marker, poll to completion, download
// the result, and assert the marker survived into the downloaded payload.
// It also asserts `exports list` contains the created id, and best-effort
// deletes the export afterward (exports is a NoUpdate resource in
// resourceRegistry, and the vendored spec DOES document DELETE
// /exports/{exportId}, but cleanup here stays best-effort per the plan
// rather than a hard assertion).
func TestExports_CreateWaitDownload(t *testing.T) {
	key := skipIfNoCreds(t)
	dataset, marker := seededData(t)
	r := NewRunner(t, key)
	logID := logIDForDataset(t, r, dataset)

	downloadPath := filepath.Join(t.TempDir(), "export.out")
	res, err := r.Run(t.Context(), "",
		"exports", "create",
		"--dataset", logID,
		"--since", "1h",
		"--where", fmt.Sprintf("ci_marker = '%s'", marker),
		"--wait",
		"--download", downloadPath,
	)
	if err != nil {
		t.Fatalf("running exports create: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exports create exited %d\nstdout: %s\nstderr: %s", res.ExitCode, res.Stdout, res.Stderr)
	}

	var final map[string]any
	if err := json.Unmarshal([]byte(res.Stdout), &final); err != nil {
		t.Fatalf("exports create --wait output did not parse as JSON: %v\noutput: %s", err, res.Stdout)
	}
	id := exportIDFromRow(final)
	if id == "" {
		t.Fatalf("export response missing export_id/id: %+v", final)
	}
	t.Cleanup(func() { bestEffortDelete(r, "exports", id) })

	payload, err := os.ReadFile(downloadPath)
	if err != nil {
		t.Fatalf("reading downloaded export: %v", err)
	}
	if len(payload) == 0 {
		t.Fatal("downloaded export file is empty")
	}
	if !bytes.Contains(payload, []byte(marker)) && !gzipContains(payload, marker) {
		t.Fatalf("downloaded export (%d bytes) does not contain marker %s", len(payload), marker)
	}

	rows := mustRunJSONArray(t, r, "exports", "list", "-o", "json")
	found := false
	for _, row := range rows {
		if exportIDFromRow(row) == id {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("exports list does not contain the created export id %s", id)
	}
}

// exportIDFromRow extracts an export's id: the vendored spec's Export
// schema uses "export_id"; "id" is accepted as a defensive fallback (see
// exportID in internal/cli/exports.go, which this mirrors).
func exportIDFromRow(row map[string]any) string {
	if v, ok := row["export_id"].(string); ok && v != "" {
		return v
	}
	if v, ok := row["id"].(string); ok && v != "" {
		return v
	}
	return ""
}

// gzipContains reports whether payload, decompressed as gzip, contains
// needle. The downloaded export's exact encoding is uncertain (the CLI
// streams the presigned object-store response verbatim — see
// downloadExport in internal/cli/exports.go), so this is a defensive
// fallback for the plain byte-containment check above.
func gzipContains(payload []byte, needle string) bool {
	if len(payload) < 2 || payload[0] != 0x1f || payload[1] != 0x8b {
		return false
	}
	zr, err := gzip.NewReader(bytes.NewReader(payload))
	if err != nil {
		return false
	}
	defer func() { _ = zr.Close() }()
	decompressed, err := io.ReadAll(zr)
	if err != nil {
		return false
	}
	return bytes.Contains(decompressed, []byte(needle))
}
