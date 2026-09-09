package codexhost

import (
	"harness.local/engorch/internal/canonical"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestR19ExactTopologyProfiles(t *testing.T) {
	path := os.Getenv("ENGORCH_R19_ATTESTATION")
	if path == "" {
		t.Skip("exact topology opt-in")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var top attestationTopology
	if err = canonical.Decode(raw, &top); err != nil {
		t.Fatal(err)
	}
	launch, err := Prepare(t.TempDir(), os.Getenv("ENGORCH_R17_BINARY"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range top.Entries {
		t.Run(entry.Profile.Role, func(t *testing.T) {
			leafPath := filepath.Join(filepath.Dir(path), filepath.FromSlash(entry.LeafFile))
			lr, err := os.ReadFile(leafPath)
			if err != nil {
				t.Fatal(err)
			}
			var leaf attestationManifest
			if err = canonical.Decode(lr, &leaf); err != nil {
				t.Fatal(err)
			}
			qr, err := os.ReadFile(filepath.Join(filepath.Dir(leafPath), leaf.QualificationFile))
			if err != nil {
				t.Fatal(err)
			}
			var proof qualificationProof
			if err = canonical.Decode(qr, &proof); err != nil {
				t.Fatal(err)
			}
			name := "final-case-e"
			if entry.Profile.Role == "reviewer" {
				name = "final-case-e-reviewer"
			}
			var tools []any
			for _, c := range proof.Cases {
				if c.Name == name {
					b, e := os.ReadFile(filepath.Join(filepath.Dir(leafPath), c.DynamicToolsFile))
					if e != nil {
						t.Fatal(e)
					}
					if e = decodeProvider(b, &tools); e != nil {
						t.Fatal(e)
					}
				}
			}
			a := CapabilityConfinementAttestation{ManifestPath: path, ExpectedSHA256: digest(raw), Profile: entry.Profile, DynamicTools: tools}
			got, err := validateAttestation(a, launch, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			lid, _ := launch.BindingID()
			want, _ := AttestationBindingID(digest(raw), lid)
			if got.id != want || got.manifestSHA != digest(raw) {
				t.Fatal("receipt not root-bound")
			}
			for _, mutation := range []string{"model", "role", "tools", "hash"} {
				bad := a
				switch mutation {
				case "model":
					bad.Profile.Model = "unqualified"
				case "role":
					bad.Profile.Role = "writer"
				case "tools":
					bad.DynamicTools = nil
				case "hash":
					bad.ExpectedSHA256 = digest([]byte("wrong"))
				}
				if _, err := validateAttestation(bad, launch, time.Now().UTC()); err == nil {
					t.Fatal("mutation admitted", mutation)
				}
			}
		})
	}
}
