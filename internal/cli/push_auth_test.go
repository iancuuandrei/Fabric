package cli

import "testing"

func TestPushTokenSelection(t *testing.T) {
	const destination = "https://github.com/fixture/project.git"
	t.Setenv("ENGORCH_FIXTURE_PUSH_TOKEN", "fixture-only")
	credentials, err := pushCredentials(destination, []string{"ENGORCH_FIXTURE_PUSH_TOKEN"})
	if err != nil || len(credentials) != 1 {
		t.Fatal("explicit token selection failed", err)
	}
	if err := credentials[0].ValidateDestination(destination); err != nil {
		t.Fatal(err)
	}
	if credentials, err := pushCredentials(destination, nil); err != nil || credentials != nil {
		t.Fatal("ambient authentication inferred")
	}
	if _, err := pushCredentials(t.TempDir(), []string{"ENGORCH_FIXTURE_PUSH_TOKEN"}); err == nil {
		t.Fatal("token sent to local destination")
	}
	if _, err := pushCredentials(destination, []string{"ENGORCH_FIXTURE_PUSH_TOKEN", "extra"}); err == nil {
		t.Fatal("extra selector ignored")
	}
}
