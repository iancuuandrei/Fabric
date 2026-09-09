package control

import "testing"

func TestConfiguredHostPolicyCannotBeSubstitutedOrOmitted(t *testing.T) {
	c := creation(t)
	strict := DefaultHostPolicy()
	strict.RequireVerifiedSandbox = true
	c.Config.HostPolicy = &strict
	if _, err := BindHostAdmission(c, hostObservation(t, false), DefaultHostPolicy()); err == nil {
		t.Fatal("configured policy was downgraded during binding")
	}
	if err := Append(t.TempDir()+"/omitted", "run.created", c); err == nil {
		t.Fatal("configured policy accepted missing admission")
	}
	unconfigured := c
	unconfigured.Config.HostPolicy = nil
	bound, err := BindHostAdmission(unconfigured, hostObservation(t, false), DefaultHostPolicy())
	if err != nil {
		t.Fatal(err)
	}
	bound.Config.HostPolicy = &strict
	if err := Append(t.TempDir()+"/substituted", "run.created", bound); err == nil {
		t.Fatal("replay admitted internally valid but substituted policy")
	}
	if err := RequireHostAdmission(Snapshot{Creation: bound}, hostObservation(t, false)); err == nil {
		t.Fatal("dispatch admitted substituted configured policy")
	}
}
