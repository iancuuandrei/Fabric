package codexhost

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"harness.local/engorch/internal/canonical"
)

func qualificationFixture(t *testing.T) (CapabilityConfinementAttestation, Launch, attestationManifest) {
	t.Helper()
	path := os.Getenv("ENGORCH_R17_ATTESTATION")
	if path == "" {
		t.Skip("opt-in exact installed-binary R17 qualification evidence")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m attestationManifest
	if err = canonical.Decode(b, &m); err != nil {
		t.Fatal(err)
	}
	var proof qualificationProof
	q, err := os.ReadFile(filepath.Join(filepath.Dir(path), filepath.FromSlash(m.QualificationFile)))
	if err != nil {
		t.Fatal(err)
	}
	if err = canonical.Decode(q, &proof); err != nil {
		t.Fatal(err)
	}
	var tools []any
	for _, c := range proof.Cases {
		if c.Name == "final-case-e" {
			raw, err := os.ReadFile(filepath.Join(filepath.Dir(path), filepath.FromSlash(c.DynamicToolsFile)))
			if err != nil {
				t.Fatal(err)
			}
			if err = decodeProvider(raw, &tools); err != nil {
				t.Fatal(err)
			}
		}
	}
	binary := os.Getenv("ENGORCH_R17_BINARY")
	if binary == "" {
		t.Fatal("exact installed Codex binary path required")
	}
	launch, err := Prepare(t.TempDir(), binary)
	if err != nil {
		t.Fatal(err)
	}
	var a CapabilityConfinementAttestation
	for _, p := range m.Profiles {
		if p.Profile.Role == "planner" {
			a = CapabilityConfinementAttestation{ManifestPath: path, ExpectedSHA256: digest(b), Profile: p.Profile, DynamicTools: tools}
		}
	}
	return a, launch, m
}

func TestR17FrozenRawEvidenceQualification(t *testing.T) {
	a, l, m := qualificationFixture(t)
	if _, err := validateAttestation(a, l, time.Now().UTC()); err != nil {
		t.Fatal("exact raw proof rejected", err)
	}
	for _, mutation := range []string{"model", "role", "tools", "expected-hash"} {
		t.Run(mutation, func(t *testing.T) {
			bad := a
			switch mutation {
			case "model":
				bad.Profile.Model = "unqualified-model"
			case "role":
				bad.Profile.Role = "explorer"
			case "tools":
				bad.DynamicTools = nil
			case "expected-hash":
				bad.ExpectedSHA256 = string(make([]byte, 64))
			}
			if _, err := validateAttestation(bad, l, time.Now().UTC()); err == nil {
				t.Fatal("drift admitted")
			}
		})
	}
	if _, err := validateAttestation(a, l, time.Now().UTC().Add(25*time.Hour)); err == nil {
		t.Fatal("expired proof admitted")
	}
	for _, mutation := range []string{"selected-record", "inventory", "missing-case", "native-spawn-output", "extra-control", "duplicate-control"} {
		t.Run(mutation, func(t *testing.T) {
			dir := t.TempDir()
			copyManifest := m
			copyManifest.Profiles = append([]attestedProfile{}, m.Profiles...)
			copyManifest.Evidence = append([]attestationEvidence{}, m.Evidence...)
			for _, e := range m.Evidence {
				b, err := os.ReadFile(filepath.Join(filepath.Dir(a.ManifestPath), filepath.FromSlash(e.Path)))
				if err != nil {
					t.Fatal(err)
				}
				p := filepath.Join(dir, filepath.FromSlash(e.Path))
				if err = os.MkdirAll(filepath.Dir(p), 0700); err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(p, b, 0600); err != nil {
					t.Fatal(err)
				}
			}
			update := func(path string, b []byte) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(path)), b, 0600); err != nil {
					t.Fatal(err)
				}
				for i := range copyManifest.Evidence {
					if copyManifest.Evidence[i].Path == path {
						copyManifest.Evidence[i].SHA256 = digest(b)
						copyManifest.Evidence[i].Bytes = int64(len(b))
					}
				}
			}
			switch mutation {
			case "extra-control", "duplicate-control":
				path := "final-clean/result.json"
				b, _ := os.ReadFile(filepath.Join(dir, filepath.FromSlash(path)))
				var result map[string]any
				if err := decodeProvider(b, &result); err != nil {
					t.Fatal(err)
				}
				command := array(result["command"])
				if mutation == "extra-control" {
					command = append(command, "-c", "agents.enabled=true")
				} else {
					command[len(command)-1] = "agents.enabled=false"
				}
				result["command"] = command
				b, err := json.Marshal(result)
				if err != nil {
					t.Fatal(err)
				}
				update(path, b)
			case "selected-record":
				copyManifest.SelectedModelRecordSHA256 = digest([]byte("wrong selected record"))
			case "inventory":
				copyManifest.Profiles[0].PreparedToolInventorySHA256 = digest([]byte("wrong inventory"))
			case "missing-case":
				b, _ := os.ReadFile(filepath.Join(dir, copyManifest.QualificationFile))
				var p qualificationProof
				if err := canonical.Decode(b, &p); err != nil {
					t.Fatal(err)
				}
				p.Cases = p.Cases[1:]
				b, _ = canonical.Bytes(p)
				update(copyManifest.QualificationFile, b)
			case "native-spawn-output":
				path := "final-ns-nohook/002-body.bin"
				b, _ := os.ReadFile(filepath.Join(dir, filepath.FromSlash(path)))
				var body map[string]any
				if err := decodeProvider(b, &body); err != nil {
					t.Fatal(err)
				}
				for _, v := range array(body["input"]) {
					item := object(v)
					if item["type"] == "function_call_output" {
						item["output"] = "spawn succeeded"
					}
				}
				b, err := json.Marshal(body)
				if err != nil {
					t.Fatal(err)
				}
				update(path, b)
			}
			b, err := canonical.Bytes(copyManifest)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "manifest.json")
			if err = os.WriteFile(path, b, 0600); err != nil {
				t.Fatal(err)
			}
			bad := a
			bad.ManifestPath = path
			bad.ExpectedSHA256 = digest(b)
			if _, err = validateAttestation(bad, l, time.Now().UTC()); err == nil {
				t.Fatal("self-consistent invalid proof admitted")
			}
		})
	}
}

func TestR17InstalledHostPreAccessAdmission(t *testing.T) {
	if os.Getenv("ENGORCH_R17_ADMIT_HOST") != "1" {
		t.Skip("opt-in no-auth no-thread installed host admission")
	}
	a, l, _ := qualificationFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	host, decision, err := StartWithCapabilityConfinementAttested(ctx, l, CapabilityConfinementRequired, a, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	if err = host.Receipt.ValidateAttested(l, decision); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(l.Root, "home", "auth.json")); !os.IsNotExist(err) {
		t.Fatal("qualification unexpectedly copied credentials", err)
	}
	if _, err = os.Stat(filepath.Join(l.Root, "home", "sessions")); !os.IsNotExist(err) {
		t.Fatal("qualification unexpectedly created sessions", err)
	}
	b, err := canonical.Bytes(struct {
		Launch   Launch                        `json:"launch"`
		Receipt  Receipt                       `json:"receipt"`
		Decision CapabilityConfinementDecision `json:"decision"`
	}{l, host.Receipt, decision})
	if err != nil {
		t.Fatal(err)
	}
	t.Log(string(b))
}
