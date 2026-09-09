package opencode

import (
	"errors"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

const (
	r11Session = "ses_f7e4c08b9ffeC90KYTcnO3Q3Oa"
	r11Message = "msg_081b3fa37002IDJ9nMqz8ORvqQ"
	r11Hash    = "7c58d95757ca5a329b4e5d38d90b23280279ec10"
)

func TestRuntimeMetadataEmptyPatchPreservesSemanticText(t *testing.T) {
	root := t.TempDir()
	expected := metadataExpectation(t, root, nil)
	raw := metadataParts("[]", "patch-empty")
	text, receipts, err := DecodeTextPartsWithRuntimeMetadata([]byte(raw), metadataAssistant(root), &expected)
	if err != nil || text != "answer" || len(receipts) != 1 || receipts[0].Classification != PatchClassificationEmpty || ValidateRuntimeMetadataForResult(&expected, receipts) != nil {
		t.Fatal("empty patch did not remain separate allowed metadata", text, receipts, err)
	}
}

func TestRuntimeMetadataSourcePatchBlocksReadOnlyResult(t *testing.T) {
	root := t.TempDir()
	expected := metadataExpectation(t, root, nil)
	files := `[` + quotePaths([]string{filepath.Join(root, "answer.md")})[0] + `]`
	_, receipts, err := DecodeTextPartsWithRuntimeMetadata([]byte(metadataParts(files, "patch-source")), metadataAssistant(root), &expected)
	var violation *RuntimeMetadataViolation
	if err != nil || len(receipts) != 1 || receipts[0].Classification != PatchClassificationWorkspaceMutation || !errors.As(ValidateRuntimeMetadataForResult(&expected, receipts), &violation) || violation.Code != MetadataViolationReadOnlyAuthority {
		t.Fatal("source patch did not fail closed as read-only authority violation", receipts, err)
	}
}

func TestRuntimeMetadataExactM1ControllerStatePatch(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("exact M1 OpenCode fixture contains native Windows paths")
	}
	files := []string{
		"D:/dev/EngOrch-M1/probe/.harness/runs/cf700552e45434971eca86e1c57d5c4fd607d06d5d38dcb7f273efb75d0ba27f.jsonl.agent-tree-shm",
		"D:/dev/EngOrch-M1/probe/.harness/runs/cf700552e45434971eca86e1c57d5c4fd607d06d5d38dcb7f273efb75d0ba27f.jsonl.agent-tree-wal",
		"D:/dev/EngOrch-M1/probe/.harness/runs/cf700552e45434971eca86e1c57d5c4fd607d06d5d38dcb7f273efb75d0ba27f.jsonl.planner.opencode-runtime.jsonl-shm",
		"D:/dev/EngOrch-M1/probe/.harness/runs/cf700552e45434971eca86e1c57d5c4fd607d06d5d38dcb7f273efb75d0ba27f.jsonl.planner.opencode-runtime.jsonl-wal",
		"D:/dev/EngOrch-M1/probe/.harness/runs/cf700552e45434971eca86e1c57d5c4fd607d06d5d38dcb7f273efb75d0ba27f.jsonl.planner.opencode-runtime.jsonl.dispatch-shm",
		"D:/dev/EngOrch-M1/probe/.harness/runs/cf700552e45434971eca86e1c57d5c4fd607d06d5d38dcb7f273efb75d0ba27f.jsonl.planner.opencode-runtime.jsonl.dispatch-wal",
		"D:/dev/EngOrch-M1/probe/.harness/runs/cf700552e45434971eca86e1c57d5c4fd607d06d5d38dcb7f273efb75d0ba27f.jsonl.planner.opencode-runtime.jsonl.state-root.jsonl-shm",
		"D:/dev/EngOrch-M1/probe/.harness/runs/cf700552e45434971eca86e1c57d5c4fd607d06d5d38dcb7f273efb75d0ba27f.jsonl.planner.opencode-runtime.jsonl.state-root.jsonl-wal",
		"D:/dev/EngOrch-M1/probe/.harness/runs/cf700552e45434971eca86e1c57d5c4fd607d06d5d38dcb7f273efb75d0ba27f.jsonl.planner.provider-gateway.jsonl",
	}
	expected := metadataExpectation(t, "D:/dev/EngOrch-M1/probe", files)
	quoted := `[` + strings.Join(quotePaths(files), ",") + `]`
	_, receipts, err := DecodeTextPartsWithRuntimeMetadata([]byte(metadataParts(quoted, "prt_081b4322b001FK8lZx0xFFmppL")), metadataAssistant("D:/dev/EngOrch-M1/probe"), &expected)
	var violation *RuntimeMetadataViolation
	if err != nil || len(receipts) != 1 || receipts[0].SnapshotHash != r11Hash || !equalStringSlices(receipts[0].Files, files) || !errors.As(ValidateRuntimeMetadataForResult(&expected, receipts), &violation) || violation.Code != MetadataViolationStateContamination {
		t.Fatal("exact M1 patch metadata was not preserved and classified", receipts, err)
	}
}

func TestRuntimeMetadataUnknownAndForgedPathsFailClosed(t *testing.T) {
	root := t.TempDir()
	controller := filepath.Join(root, ".harness", "runs", "run.jsonl-wal")
	expected := metadataExpectation(t, root, []string{controller})
	unknown := strings.Replace(metadataParts("[]", "patch-empty"), `"type":"patch"`, `"type":"future-metadata"`, 1)
	if _, _, err := DecodeTextPartsWithRuntimeMetadata([]byte(unknown), metadataAssistant(root), &expected); err == nil {
		t.Fatal("unknown part passed metadata decoder")
	}
	outside := filepath.Join(t.TempDir(), "forged.txt")
	for _, paths := range [][]string{{outside}, {filepath.Join(root, "source.txt"), controller}} {
		files := `[` + strings.Join(quotePaths(paths), ",") + `]`
		if _, _, err := DecodeTextPartsWithRuntimeMetadata([]byte(metadataParts(files, "patch-forged")), metadataAssistant(root), &expected); err == nil {
			t.Fatal("forged or mixed patch paths passed", files)
		}
	}
}

func metadataExpectation(t *testing.T, root string, paths []string) RuntimeMetadataExpectation {
	t.Helper()
	expected, err := NewRuntimeMetadataExpectation(MetadataAuthorityReadOnly, root, paths)
	if err != nil {
		t.Fatal(err)
	}
	return expected
}

func metadataAssistant(root string) Assistant {
	return Assistant{ID: r11Message, Binding: Binding{SessionID: r11Session, Directory: root}}
}

func metadataParts(files, partID string) string {
	return `[{"id":"text-r11","sessionID":"` + r11Session + `","messageID":"` + r11Message + `","type":"text","text":"answer","time":{"start":1,"end":2}},{"id":"` + partID + `","sessionID":"` + r11Session + `","messageID":"` + r11Message + `","type":"patch","hash":"` + r11Hash + `","files":` + files + `}]`
}

func quotePaths(paths []string) []string {
	result := make([]string, len(paths))
	for index, path := range paths {
		result[index] = strconv.Quote(path)
	}
	return result
}

func equalStringSlices(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
