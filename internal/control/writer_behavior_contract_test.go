package control

import (
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/runtime"
	"github.com/iancuuandrei/fabric/internal/writercontract"
)

func TestWriterBehaviorPreservationGuidanceAcrossContracts(t *testing.T) {
	contracts := []string{
		"", "nonempty-v1", "utf8-v2", writercontract.ContractChangesJSONV1,
		writercontract.ContractUTF8ReplaceV3, writercontract.ContractUTF8ScopedV4,
		writercontract.ContractAnchoredEditsV1, writercontract.ContractAnchoredEditsV2,
		writercontract.ContractAnchoredEditsV3,
	}
	required := []string{
		"Preserve established behavior outside the requested feature.",
		"Compare base and candidate behavior at relevant boundaries",
		"a focused regression test that distinguishes the intended change from nearby unchanged cases",
		"explicit lexical boundaries rather than lookahead heuristics",
		"quote, escape, comment, and end-of-input transitions",
	}
	for _, contract := range contracts {
		t.Run(contractOrDefault(contract), func(t *testing.T) {
			invocation := writerPromptInvocationForContract(t, contract)
			for _, text := range required {
				if !strings.Contains(invocation.Input, text) {
					t.Errorf("writer contract %q omitted shared behavior guidance %q", contract, text)
				}
			}
		})
	}
}

func TestFixerReceivesBehaviorPreservationGuidance(t *testing.T) {
	path, _, _, _ := anchoredWriterV3Fixture(t)
	snapshot, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Creation.Config.Version = 2
	fixer := *snapshot.Creation.Config.Writer
	fixer.Role = "fixer"
	snapshot.Creation.Config.Fixer = &fixer
	snapshot.State = "REPAIRING"
	invocation, err := writerInvocation(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if invocation.Profile.Role != "fixer" {
		t.Fatalf("repair invocation role = %q, want fixer", invocation.Profile.Role)
	}
	for _, text := range []string{
		"Preserve established behavior outside the requested feature.",
		"a focused regression test that distinguishes the intended change from nearby unchanged cases",
		"explicit lexical boundaries rather than lookahead heuristics",
	} {
		if !strings.Contains(invocation.Input, text) {
			t.Errorf("fixer instruction omitted shared behavior guidance %q", text)
		}
	}
}

func writerPromptInvocationForContract(t *testing.T, contract string) runtime.Invocation {
	t.Helper()
	switch contract {
	case writercontract.ContractAnchoredEditsV2:
		_, invocation, _, _ := anchoredWriterV2Fixture(t)
		return invocation
	case writercontract.ContractAnchoredEditsV3:
		_, invocation, _, _ := anchoredWriterV3Fixture(t)
		return invocation
	default:
		_, invocation := writerInvocationFixtureForContract(t, contract)
		return invocation
	}
}

func contractOrDefault(contract string) string {
	if contract == "" {
		return "default"
	}
	return contract
}
