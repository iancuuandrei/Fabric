package cli

import (
	"context"
	"io"

	"harness.local/engorch/internal/control"
	"harness.local/engorch/internal/hostenvironment"
	"harness.local/engorch/internal/repository"
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
	})
}
