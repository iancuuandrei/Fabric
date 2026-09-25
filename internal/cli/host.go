package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"

	"harness.local/engorch/internal/config"
	"harness.local/engorch/internal/control"
	"harness.local/engorch/internal/hostenvironment"
	"harness.local/engorch/internal/repository"
	"harness.local/engorch/internal/runtime"
)

type hostObserver func(context.Context) (hostenvironment.Observation, error)

func bindCurrentHost(ctx context.Context, creation control.Creation) (control.Creation, error) {
	observation, err := hostenvironment.ObserveDefault(ctx)
	if err != nil {
		return control.Creation{}, err
	}
	policy := control.DefaultHostPolicy()
	if creation.Config.HostPolicy != nil {
		policy = *creation.Config.HostPolicy
	}
	return control.BindHostAdmission(creation, observation, policy)
}

func writeDoctor(ctx context.Context, out io.Writer, identity repository.Identity, policy *hostenvironment.Policy) error {
	return writeDoctorWithObserver(ctx, out, identity, hostenvironment.ObserveDefault, policy)
}

func writeDoctorWithObserver(ctx context.Context, out io.Writer, identity repository.Identity, observe hostObserver, policy *hostenvironment.Policy) error {
	host, err := observe(ctx)
	if err != nil {
		return err
	}
	selected := control.DefaultHostPolicy()
	if policy != nil {
		selected = *policy
	}
	if err := hostenvironment.Admit(selected, host); err != nil {
		return err
	}
	return output(out, map[string]any{
		"status":           "PASS",
		"repository":       identity,
		"host_environment": host,
		"runtime_dispatch": "NOT_RUN",
		"verification":     "NOT_RUN",
		"route_readiness":  openCodeRouteReadiness(nil),
	})
}

func openCodeRoleEntries(cfg *config.Config) []map[string]string {
	roles := []map[string]string{}
	if cfg == nil {
		return roles
	}
	add := func(role string, p *runtime.Profile) {
		if p == nil {
			return
		}
		if p.Runtime != "opencode-http" {
			return
		}
		roles = append(roles, map[string]string{
			"role":     role,
			"provider": p.Provider,
			"model":    p.Model,
			"runtime":  p.Runtime,
		})
	}
	planner := cfg.Planner
	if planner.Runtime == "opencode-http" {
		roles = append(roles, map[string]string{
			"role":     "planner",
			"provider": planner.Provider,
			"model":    planner.Model,
			"runtime":  planner.Runtime,
		})
	}
	add("writer", cfg.Writer)
	add("fixer", cfg.Fixer)
	add("explorer", cfg.Explorer)
	add("reviewer", cfg.Reviewer)
	return roles
}

func openCodeCleanAbsoluteNonVolumeRoot(p string) bool {
	if p == "" || !filepath.IsAbs(p) {
		return false
	}
	if filepath.Clean(p) != p {
		return false
	}
	if p == filepath.VolumeName(p)+string(filepath.Separator) {
		return false
	}
	return true
}

func openCodeRouteReadiness(cfg *config.Config) map[string]any {
	roles := openCodeRoleEntries(cfg)
	if roles == nil {
		roles = []map[string]string{}
	}
	present := cfg != nil && cfg.OpenCode != nil
	if !present && len(roles) == 0 {
		return map[string]any{"status": "NOT_CHECKED", "reason": "opencode host not configured", "roles": roles}
	}
	if present && len(roles) == 0 {
		return map[string]any{"status": "NOT_READY", "reason": "opencode host without opencode-http roles", "roles": roles}
	}
	if !present && len(roles) > 0 {
		return map[string]any{"status": "NOT_READY", "reason": "opencode-http roles without opencode host", "roles": roles}
	}
	exe := cfg.OpenCode.Executable
	state := cfg.OpenCode.StateRoot
	want := strings.ToLower(strings.TrimSpace(cfg.OpenCode.ExecutableHash))
	if !openCodeCleanAbsoluteNonVolumeRoot(exe) || !openCodeCleanAbsoluteNonVolumeRoot(state) {
		return map[string]any{"status": "NOT_READY", "reason": "opencode host path invalid", "roles": roles}
	}
	fi, err := os.Lstat(exe)
	if err != nil || !fi.Mode().IsRegular() || fi.Mode()&os.ModeSymlink != 0 || fi.Mode()&os.ModeCharDevice != 0 || fi.Mode()&os.ModeDir != 0 {
		return map[string]any{"status": "NOT_READY", "reason": "opencode executable unavailable", "roles": roles}
	}
	f, err := os.Open(exe)
	if err != nil {
		return map[string]any{"status": "NOT_READY", "reason": "opencode executable unavailable", "roles": roles}
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		_ = f.Close()
		return map[string]any{"status": "NOT_READY", "reason": "opencode executable unreadable", "roles": roles}
	}
	if err := f.Close(); err != nil {
		return map[string]any{"status": "NOT_READY", "reason": "opencode executable unreadable", "roles": roles}
	}
	if hex.EncodeToString(h.Sum(nil)) != want {
		return map[string]any{"status": "NOT_READY", "reason": "opencode executable hash mismatch", "roles": roles}
	}
	st, err := os.Lstat(state)
	if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return map[string]any{"status": "NOT_READY", "reason": "opencode state unavailable", "roles": roles}
	}
	return map[string]any{"status": "READY", "reason": "opencode local prerequisites ready", "roles": roles}
}

func writeDoctorWithConfig(ctx context.Context, out io.Writer, identity repository.Identity, policy *hostenvironment.Policy, cfg *config.Config) error {
	return writeDoctorWithConfigObserver(ctx, out, identity, hostenvironment.ObserveDefault, policy, cfg)
}

func writeDoctorWithConfigObserver(ctx context.Context, out io.Writer, identity repository.Identity, observe hostObserver, policy *hostenvironment.Policy, cfg *config.Config) error {
	host, err := observe(ctx)
	if err != nil {
		return err
	}
	selected := control.DefaultHostPolicy()
	if policy != nil {
		selected = *policy
	}
	if err := hostenvironment.Admit(selected, host); err != nil {
		return err
	}
	return output(out, map[string]any{
		"status":           "PASS",
		"repository":       identity,
		"host_environment": host,
		"runtime_dispatch": "NOT_RUN",
		"verification":     "NOT_RUN",
		"route_readiness":  openCodeRouteReadiness(cfg),
	})
}
