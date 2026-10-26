package checks

import (
	"context"
	"errors"
	"testing"

	health "github.com/jamesread/armature-health"
)

func keys(t *testing.T, check health.Check) []string {
	t.Helper()
	issues, err := check.Run(context.Background())
	if err != nil {
		t.Fatalf("%s: %v", check.ID(), err)
	}
	var out []string
	for _, issue := range issues {
		out = append(out, issue.Key)
	}
	return out
}

func TestDefaultPassword(t *testing.T) {
	check := DefaultPassword{Product: "Test", UsesDefaultPassword: func(context.Context) (bool, error) { return true, nil }}
	if got := keys(t, check); len(got) != 1 || got[0] != "admin" {
		t.Fatalf("got %v", got)
	}

	check.UsesDefaultPassword = func(context.Context) (bool, error) { return false, nil }
	if got := keys(t, check); len(got) != 0 {
		t.Fatalf("got %v", got)
	}

	check.UsesDefaultPassword = func(context.Context) (bool, error) { return false, errors.New("store down") }
	if _, err := check.Run(context.Background()); err == nil {
		t.Fatal("expected the lookup error to fail the check")
	}
}

func TestRiskySettings(t *testing.T) {
	env := map[string]string{"RESET": "1"}
	check := RiskySettings{
		Getenv:   func(k string) string { return env[k] },
		EnvVars:  []RiskyEnvVar{{Name: "RESET"}, {Name: "UNSET"}},
		LogLevel: "DEBUG",
	}
	issues, _ := check.Run(context.Background())
	if len(issues) != 2 || issues[0].Key != "env:RESET" || issues[0].Severity != health.SeverityWarning || issues[1].Key != "log-level" {
		t.Fatalf("got %+v", issues)
	}

	check.LogLevel = "info"
	env = nil
	if got := keys(t, check); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}
