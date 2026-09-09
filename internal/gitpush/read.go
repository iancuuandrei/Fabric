package gitpush

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type capture struct {
	buffer   bytes.Buffer
	limit    int
	overflow bool
}

// Write drains output while retaining a bounded prefix and an overflow flag.
func (c *capture) Write(data []byte) (int, error) {
	remaining := c.limit - c.buffer.Len()
	keep := len(data)
	if keep > remaining {
		keep = remaining
		c.overflow = true
	}
	_, _ = c.buffer.Write(data[:keep])
	return len(data), nil
}

func isolatedEnvironment(directory string) []string {
	environment := []string{}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(strings.ToUpper(key), "GIT_") {
			environment = append(environment, entry)
		}
	}
	return append(environment,
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_SYSTEM="+os.DevNull,
		"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_TERMINAL_PROMPT=0",
		"GIT_CEILING_DIRECTORIES="+filepath.Dir(directory),
		"GIT_NO_LAZY_FETCH=1", "GIT_OPTIONAL_LOCKS=0")
}

// ObserveRemote reads one explicit branch through Git in a temporary directory
// outside repository configuration. It disables ambient Git configuration,
// credential helpers and redirects. It never fetches objects or changes refs.
// An error returns no observation; an absent ref is not proof of non-execution.
func ObserveRemote(ctx context.Context, plan Plan) (observation RefObservation, err error) {
	return observeRemote(ctx, plan, nil)
}

// ObserveRemoteAuthenticated uses an explicitly destination-bound credential.
// It retains the same non-mutating and bounded observation contract.
func ObserveRemoteAuthenticated(ctx context.Context, plan Plan, credential *Credential) (RefObservation, error) {
	if credential == nil {
		return RefObservation{}, errors.New("explicit GitHub credential required")
	}
	return observeRemote(ctx, plan, credential)
}

func observeRemote(ctx context.Context, plan Plan, credential *Credential) (observation RefObservation, err error) {
	if err := credential.validate(plan.Destination); err != nil {
		return observation, err
	}
	if _, err := plan.ID(); err != nil {
		return observation, err
	}
	if err := ctx.Err(); err != nil {
		return observation, err
	}
	directory, err := os.MkdirTemp("", "engorch-remote-read-")
	if err != nil {
		return observation, err
	}
	defer func() {
		if closeErr := os.Remove(directory); closeErr != nil {
			observation = RefObservation{}
			err = errors.Join(err, closeErr)
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "git", "--no-optional-locks",
		"-c", "protocol.allow=never", "-c", "protocol.file.allow=always",
		"-c", "protocol.https.allow=always", "-c", "http.followRedirects=false",
		"-c", "credential.helper=", "-c", "core.askPass=",
		"ls-remote", "--refs", "--", plan.Destination, plan.TargetRef)
	command.Dir = directory
	command.Env = credential.environment(isolatedEnvironment(directory))
	command.WaitDelay = time.Second
	stdout, stderr := &capture{limit: 2048}, &capture{limit: 4096}
	command.Stdout, command.Stderr = stdout, stderr
	if err := command.Run(); err != nil {
		return RefObservation{}, errors.Join(errors.New("remote ref observation failed"), ctx.Err())
	}
	if stdout.overflow || stderr.overflow {
		return RefObservation{}, errors.New("remote ref observation exceeded output bounds")
	}
	return ParseAdvertisement(plan, stdout.buffer.Bytes())
}
