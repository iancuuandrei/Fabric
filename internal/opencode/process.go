package opencode

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/safepath"
)

// Process owns one local OpenCode root process. It grants no dispatch authority
// and does not claim descendant confinement or server configuration admission.
type Process struct {
	command          *exec.Cmd
	done             chan struct{}
	cancel           context.CancelFunc
	once             sync.Once
	err              error
	// mu guards admittedTools and admittedProvider, which are written once
	// during readiness admission and read concurrently via identity.
	mu               sync.RWMutex
	launch           processLaunchIdentity
	admittedTools    *ToolsConfigurationReceipt
	admittedProvider *ProviderConfigurationReceipt
}

type processToolLaunchIdentity struct {
	Endpoint              string
	BearerSHA256          string
	ToolIDs               []string
	TimeoutMillis         int
	AllowStructuredOutput bool `json:"allow_structured_output,omitempty"`
}

type processLaunchIdentity struct {
	ExecutableSHA256 string
	Root             string
	WorkingDirectory string
	Endpoint         string
	AuthSHA256       string
	Tools            *processToolLaunchIdentity
	MaxOutputTokens  int64
	Diagnostic       bool
	AdmittedTools    *ToolsConfigurationReceipt
	Provider         *processProviderLaunchIdentity
	AdmittedProvider *ProviderConfigurationReceipt
}

// StartupAttempt records the terminal result of one pre-dispatch host attempt.
// Configuration receipts remain present when exact /config admission succeeded
// but a later project readiness gate rejected the same process.
type StartupAttempt struct {
	Number                   int
	Outcome                  string
	Configuration            HostConfiguration
	ToolsConfiguration       ToolsConfigurationReceipt
	ProviderConfiguration    ProviderConfigurationReceipt
	Reaped                   bool
	ReadinessElapsedMillis   int64
	ReadinessPolls           int
	ReadinessHTTPObserved    bool
	FirstHTTPResponseMillis  int64
	FinalReadinessErrorClass string
	TCPObserved              bool
	FirstTCPMillis           int64
	HealthHTTPObserved       bool
	FirstHealthHTTPMillis    int64
	FinalHealthErrorClass    string
	ProcessExited            bool
	ProjectReadinessPolls    int
	ProjectReadinessMillis   int64
	Err                      error
}

type readinessObservation struct {
	elapsedMillis           int64
	polls                   int
	httpObserved            bool
	firstHTTPResponseMillis int64
	finalErrorClass         string
	tcpObserved             bool
	firstTCPMillis          int64
	healthHTTPObserved      bool
	firstHealthHTTPMillis   int64
	finalHealthErrorClass   string
	processExited           bool
	projectReadinessPolls   int
	projectReadinessMillis  int64
}

type readinessDiagnostics struct {
	cancel context.CancelFunc
	done   <-chan readinessObservation
}

type startupAdmission struct {
	host     HostConfiguration
	tools    ToolsConfigurationReceipt
	provider ProviderConfigurationReceipt
}

// StartProcess starts the pinned binary in an already prepared private root.
// The caller must subsequently admit server identity/configuration before use.
// Provider authentication is not inherited by this bootstrap operation.
func StartProcess(ctx context.Context, binary, digest, root string, port int, user, password string, output io.Writer) (*Process, error) {
	return startProcess(ctx, binary, digest, root, root, port, user, password, output, false, 0, nil, nil, "")
}

// StartLimitedProcess supplies an explicit per-generation output limit to the
// pinned runtime. Provider enforcement and total input/turn accounting require
// independent qualification; this setting alone is not a total token budget.
func StartLimitedProcess(ctx context.Context, binary, digest, root string, port int, user, password string, output io.Writer, maxOutputTokens int64) (*Process, error) {
	if maxOutputTokens < 1 || maxOutputTokens > 9007199254740991 {
		return nil, errors.New("invalid output token limit")
	}
	return startProcess(ctx, binary, digest, root, root, port, user, password, output, false, maxOutputTokens, nil, nil, "")
}

// StartReadyLimitedProcess starts and admits a fresh host before any session or
// dispatch exists. A stalled instance is fully reaped before the optional second
// attempt because the pinned server caches an unresolved instance bootstrap.
// The caller must supply a total context deadline and a per-attempt readiness
// bound. This bootstrap retry policy must not be reused for uncertain dispatch.
func StartReadyLimitedProcess(ctx context.Context, binary, digest, root string, port int, user, password string, output io.Writer, maxOutputTokens int64, attempts int, readinessTimeout time.Duration) (*Process, HostConfiguration, []StartupAttempt, error) {
	return startReadyLimitedProcess(ctx, binary, digest, root, port, user, password, output, maxOutputTokens, attempts, readinessTimeout, false)
}

func startReadyLimitedProcess(ctx context.Context, binary, digest, root string, port int, user, password string, output io.Writer, maxOutputTokens int64, attempts int, readinessTimeout time.Duration, diagnostic bool) (*Process, HostConfiguration, []StartupAttempt, error) {
	process, admission, evidence, err := startReadyLimitedProcessWithAdmission(ctx, binary, digest, root, port, user, password, output, maxOutputTokens, attempts, readinessTimeout, diagnostic, nil, nil, "", func(raw []byte) (startupAdmission, error) {
		configuration, err := decodeHostConfiguration(raw)
		return startupAdmission{host: configuration}, err
	})
	return process, admission.host, evidence, err
}

// StartReadyLimitedToolsProcess starts a bounded pre-dispatch host configured
// for one validated controller-owned MCP server and admits its exact readback.
// The default process APIs remain deny-only and carry no MCP configuration.
func StartReadyLimitedToolsProcess(ctx context.Context, binary, digest, root string, port int, user, password string, output io.Writer, maxOutputTokens int64, attempts int, readinessTimeout time.Duration, tools ToolsConfigurationSpec) (*Process, ToolsConfigurationReceipt, []StartupAttempt, error) {
	// Keep launch and readback bound to the same caller-independent snapshot.
	tools.ToolNames = append([]string(nil), tools.ToolNames...)
	process, admission, evidence, err := startReadyLimitedProcessWithAdmission(ctx, binary, digest, root, port, user, password, output, maxOutputTokens, attempts, readinessTimeout, false, &tools, nil, "", func(raw []byte) (startupAdmission, error) {
		configuration, err := decodeToolsConfiguration(raw, tools)
		return startupAdmission{tools: configuration}, err
	})
	return process, admission.tools, evidence, err
}

func startReadyLimitedProcessWithAdmission(ctx context.Context, binary, digest, root string, port int, user, password string, output io.Writer, maxOutputTokens int64, attempts int, readinessTimeout time.Duration, diagnostic bool, tools *ToolsConfigurationSpec, provider *ProviderConfigurationSpec, configContent string, decode func([]byte) (startupAdmission, error)) (*Process, startupAdmission, []StartupAttempt, error) {
	return startReadyLimitedProcessWithAdmissionInDirectory(ctx, binary, digest, root, root, port, user, password, output, maxOutputTokens, attempts, readinessTimeout, diagnostic, tools, provider, configContent, nil, decode)
}

func startReadyLimitedProcessWithAdmissionInDirectory(ctx context.Context, binary, digest, root, workingDirectory string, port int, user, password string, output io.Writer, maxOutputTokens int64, attempts int, readinessTimeout time.Duration, diagnostic bool, tools *ToolsConfigurationSpec, provider *ProviderConfigurationSpec, configContent string, project *ProjectExpectation, decode func([]byte) (startupAdmission, error)) (*Process, startupAdmission, []StartupAttempt, error) {
	if ctx == nil || attempts < 1 || attempts > 2 || readinessTimeout <= 0 || maxOutputTokens < 1 || maxOutputTokens > 9007199254740991 {
		return nil, startupAdmission{}, nil, errors.New("invalid OpenCode readiness policy")
	}
	if _, ok := ctx.Deadline(); !ok {
		return nil, startupAdmission{}, nil, errors.New("OpenCode readiness requires total deadline")
	}
	if project != nil && (project.validate() != nil || project.Directory != filepath.Clean(workingDirectory)) {
		return nil, startupAdmission{}, nil, errors.New("OpenCode readiness project differs from working directory")
	}
	evidence := make([]StartupAttempt, 0, attempts)
	for number := 1; number <= attempts; number++ {
		attemptCtx, stopAttempt := context.WithTimeout(ctx, readinessTimeout)
		process, err := startProcess(ctx, binary, digest, root, workingDirectory, port, user, password, output, diagnostic, maxOutputTokens, tools, provider, configContent)
		if err != nil {
			stopAttempt()
			evidence = append(evidence, StartupAttempt{Number: number, Outcome: "start_failed", Err: err})
			if ctx.Err() != nil {
				return nil, startupAdmission{}, evidence, errors.Join(errors.New("OpenCode readiness deadline exhausted"), ctx.Err())
			}
			continue
		}
		client, err := NewClient("http://127.0.0.1:"+strconv.Itoa(port), user, password)
		if err != nil {
			reaped, closeErr := reapStartupProcess(process)
			err = errors.Join(err, closeErr)
			stopAttempt()
			evidence = append(evidence, StartupAttempt{Number: number, Outcome: "client_failed", Reaped: reaped, Err: err})
			return nil, startupAdmission{}, evidence, err
		}
		healthObservation, healthOutcome, healthErr := waitForHealthy(attemptCtx, process, client)
		if healthErr != nil {
			reaped, closeErr := reapStartupProcess(process)
			healthErr = errors.Join(healthErr, closeErr)
			client.Close()
			stopAttempt()
			evidence = append(evidence, startupAttempt(number, healthOutcome, reaped, healthObservation, startupAdmission{}, healthErr))
			if ctx.Err() != nil {
				return nil, startupAdmission{}, evidence, errors.Join(errors.New("OpenCode readiness deadline exhausted"), ctx.Err())
			}
			continue
		}
		var diagnostics *readinessDiagnostics
		if diagnostic {
			diagnostics = startReadinessDiagnostics(attemptCtx, client)
		}
		configuration, outcome, observation, err := admitProcessWith(attemptCtx, process, client, decode)
		observation.projectReadinessPolls = healthObservation.projectReadinessPolls
		observation.projectReadinessMillis = healthObservation.projectReadinessMillis
		observation.elapsedMillis += healthObservation.elapsedMillis
		if observation.httpObserved {
			observation.firstHTTPResponseMillis += healthObservation.elapsedMillis
		}
		observation.healthHTTPObserved = healthObservation.healthHTTPObserved
		observation.firstHealthHTTPMillis = healthObservation.firstHealthHTTPMillis
		observation.finalHealthErrorClass = healthObservation.finalHealthErrorClass
		if diagnostics != nil {
			observation = mergeReadinessDiagnostics(observation, diagnostics.stop())
		}
		if err == nil && project != nil {
			_, observation.projectReadinessPolls, observation.projectReadinessMillis, err = waitForProjectAdmission(attemptCtx, process, client, *project)
			if err != nil {
				outcome = "project_rejected"
			}
		}
		client.Close()
		if err == nil {
			if tools != nil {
				if err := process.setAdmittedTools(configuration.tools); err != nil {
					reaped, closeErr := reapStartupProcess(process)
					err = errors.Join(err, closeErr)
					stopAttempt()
					evidence = append(evidence, startupAttempt(number, "configuration_rejected", reaped, observation, startupAdmission{}, err))
					return nil, startupAdmission{}, evidence, err
				}
			}
			if provider != nil {
				if err := process.setAdmittedProvider(configuration.provider); err != nil {
					reaped, closeErr := reapStartupProcess(process)
					err = errors.Join(err, closeErr)
					stopAttempt()
					evidence = append(evidence, startupAttempt(number, "configuration_rejected", reaped, observation, startupAdmission{}, err))
					return nil, startupAdmission{}, evidence, err
				}
			}
			stopAttempt()
			evidence = append(evidence, startupAttempt(number, outcome, false, observation, configuration, nil))
			return process, configuration, evidence, nil
		}
		reaped, closeErr := reapStartupProcess(process)
		err = errors.Join(err, closeErr)
		stopAttempt()
		failedAdmission := startupAdmission{}
		if outcome == "project_rejected" {
			failedAdmission = configuration
		}
		evidence = append(evidence, startupAttempt(number, outcome, reaped, observation, failedAdmission, err))
		if outcome == "configuration_rejected" || outcome == "project_rejected" {
			return nil, startupAdmission{}, evidence, err
		}
		if ctx.Err() != nil {
			return nil, startupAdmission{}, evidence, errors.Join(errors.New("OpenCode readiness deadline exhausted"), ctx.Err())
		}
	}
	return nil, startupAdmission{}, evidence, errors.New("OpenCode readiness attempts exhausted")
}

func reapStartupProcess(process *Process) (bool, error) {
	if process == nil {
		return false, errors.New("OpenCode startup process is unavailable for reaping")
	}
	err := process.Close()
	select {
	case <-process.Done():
		return true, err
	default:
		return false, errors.Join(err, errors.New("OpenCode startup process was not reaped"))
	}
}

const startupProjectReadinessMaximum = 5 * time.Second

func waitForProjectAdmission(ctx context.Context, process *Process, client *Client, expected ProjectExpectation) (ProjectReceipt, int, int64, error) {
	started := time.Now()
	readinessCtx, cancel := context.WithTimeout(ctx, startupProjectReadinessMaximum)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var last error
	polls := 0
	for {
		project, err := client.ReadCurrentProject(readinessCtx, expected)
		polls++
		if err == nil {
			return project, polls, time.Since(started).Milliseconds(), nil
		}
		if !errors.Is(err, ErrProjectGlobalIdentity) {
			return ProjectReceipt{}, polls, time.Since(started).Milliseconds(), err
		}
		last = err
		select {
		case <-process.Done():
			return ProjectReceipt{}, polls, time.Since(started).Milliseconds(), errors.Join(errors.New("OpenCode exited before project readiness"), last, process.Wait())
		case <-readinessCtx.Done():
			return ProjectReceipt{}, polls, time.Since(started).Milliseconds(), errors.Join(errors.New("OpenCode Git project identity did not become ready"), last, readinessCtx.Err())
		case <-ticker.C:
		}
	}
}

func waitForHealthy(ctx context.Context, process *Process, client *Client) (readinessObservation, string, error) {
	started := time.Now()
	observation := readinessObservation{}
	finish := func() readinessObservation {
		observation.elapsedMillis = time.Since(started).Milliseconds()
		return observation
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		probeCtx, stopProbe := context.WithTimeout(ctx, 500*time.Millisecond)
		httpObserved, class := client.diagnosticHealth(probeCtx)
		stopProbe()
		if httpObserved && !observation.healthHTTPObserved {
			observation.healthHTTPObserved = true
			observation.firstHealthHTTPMillis = time.Since(started).Milliseconds()
		}
		observation.finalHealthErrorClass = class
		if class == "" {
			return finish(), "healthy", nil
		}
		select {
		case <-process.Done():
			observation.processExited = true
			return finish(), "exited", errors.Join(errors.New("OpenCode exited before health readiness"), process.Wait())
		case <-ctx.Done():
			return finish(), "timeout", errors.Join(errors.New("OpenCode health unavailable"), ctx.Err())
		case <-ticker.C:
		}
	}
}

func admitProcess(ctx context.Context, process *Process, client *Client) (HostConfiguration, string, error) {
	configuration, outcome, _, err := admitProcessWith(ctx, process, client, func(raw []byte) (startupAdmission, error) {
		host, err := decodeHostConfiguration(raw)
		return startupAdmission{host: host}, err
	})
	return configuration.host, outcome, err
}

func admitProcessWith(ctx context.Context, process *Process, client *Client, decode func([]byte) (startupAdmission, error)) (startupAdmission, string, readinessObservation, error) {
	started := time.Now()
	observation := readinessObservation{}
	finish := func() readinessObservation {
		observation.elapsedMillis = time.Since(started).Milliseconds()
		return observation
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		raw, err := client.readConfiguration(ctx, process.launch.WorkingDirectory)
		observation.polls++
		if err == nil {
			observation.httpObserved = true
			observation.firstHTTPResponseMillis = time.Since(started).Milliseconds()
			observation.finalErrorClass = ""
			configuration, decodeErr := decode(raw)
			if decodeErr != nil {
				return startupAdmission{}, "configuration_rejected", finish(), decodeErr
			}
			select {
			case <-process.Done():
				observation.processExited = true
				return startupAdmission{}, "exited", finish(), errors.Join(errors.New("OpenCode exited during readiness"), process.Wait())
			default:
				return configuration, "ready", finish(), nil
			}
		}
		observation.finalErrorClass = readinessErrorClass(err)
		select {
		case <-process.Done():
			observation.processExited = true
			return startupAdmission{}, "exited", finish(), errors.Join(errors.New("OpenCode exited before readiness"), process.Wait())
		case <-ctx.Done():
			return startupAdmission{}, "timeout", finish(), errors.Join(errors.New("OpenCode configuration unavailable"), ctx.Err())
		case <-ticker.C:
		}
	}
}

func startupAttempt(number int, outcome string, reaped bool, observation readinessObservation, configuration startupAdmission, err error) StartupAttempt {
	return StartupAttempt{Number: number, Outcome: outcome, Configuration: configuration.host, ToolsConfiguration: configuration.tools, ProviderConfiguration: configuration.provider, Reaped: reaped, ReadinessElapsedMillis: observation.elapsedMillis, ReadinessPolls: observation.polls, ReadinessHTTPObserved: observation.httpObserved, FirstHTTPResponseMillis: observation.firstHTTPResponseMillis, FinalReadinessErrorClass: observation.finalErrorClass, TCPObserved: observation.tcpObserved, FirstTCPMillis: observation.firstTCPMillis, HealthHTTPObserved: observation.healthHTTPObserved, FirstHealthHTTPMillis: observation.firstHealthHTTPMillis, FinalHealthErrorClass: observation.finalHealthErrorClass, ProcessExited: observation.processExited, ProjectReadinessPolls: observation.projectReadinessPolls, ProjectReadinessMillis: observation.projectReadinessMillis, Err: err}
}

func startReadinessDiagnostics(ctx context.Context, admissionClient *Client) *readinessDiagnostics {
	diagnosticCtx, cancel := context.WithCancel(ctx)
	done := make(chan readinessObservation, 1)
	go func() {
		started := time.Now()
		observation := readinessObservation{}
		client, err := NewClient(admissionClient.base, admissionClient.user, admissionClient.password)
		if err != nil {
			observation.finalHealthErrorClass = "other"
			done <- observation
			return
		}
		defer client.Close()
		address := strings.TrimPrefix(admissionClient.base, "http://")
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			probeCtx, stopProbe := context.WithTimeout(diagnosticCtx, 500*time.Millisecond)
			connection, dialErr := (&net.Dialer{Timeout: 500 * time.Millisecond}).DialContext(probeCtx, "tcp4", address)
			if dialErr == nil {
				_ = connection.Close()
				if !observation.tcpObserved {
					observation.tcpObserved = true
					observation.firstTCPMillis = time.Since(started).Milliseconds()
				}
				healthHTTPObserved, healthClass := client.diagnosticHealth(probeCtx)
				if healthClass == "" {
					if !observation.healthHTTPObserved {
						observation.healthHTTPObserved = true
						observation.firstHealthHTTPMillis = time.Since(started).Milliseconds()
					}
					observation.finalHealthErrorClass = ""
				} else {
					if errors.Is(diagnosticCtx.Err(), context.Canceled) && strings.HasSuffix(healthClass, "-canceled") {
						stopProbe()
						done <- observation
						return
					}
					if healthHTTPObserved && !observation.healthHTTPObserved {
						observation.healthHTTPObserved = true
						observation.firstHealthHTTPMillis = time.Since(started).Milliseconds()
					}
					observation.finalHealthErrorClass = healthClass
				}
			} else {
				observation.finalHealthErrorClass = readinessErrorClass(readbackFailure{class: networkReadinessClass(probeCtx, dialErr), cause: probeCtx.Err()})
			}
			stopProbe()
			select {
			case <-diagnosticCtx.Done():
				done <- observation
				return
			case <-ticker.C:
			}
		}
	}()
	return &readinessDiagnostics{cancel: cancel, done: done}
}

func (d *readinessDiagnostics) stop() readinessObservation {
	d.cancel()
	return <-d.done
}

func mergeReadinessDiagnostics(observation, diagnostic readinessObservation) readinessObservation {
	observation.tcpObserved = diagnostic.tcpObserved
	observation.firstTCPMillis = diagnostic.firstTCPMillis
	if !observation.healthHTTPObserved && diagnostic.healthHTTPObserved {
		observation.healthHTTPObserved = true
		observation.firstHealthHTTPMillis = diagnostic.firstHealthHTTPMillis
	}
	if diagnostic.finalHealthErrorClass != "" {
		observation.finalHealthErrorClass = diagnostic.finalHealthErrorClass
	}
	return observation
}

func readinessErrorClass(err error) string {
	var classified interface{ readinessClass() string }
	if errors.As(err, &classified) {
		return classified.readinessClass()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "deadline"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	return "other"
}

func startProcess(ctx context.Context, binary, digest, root, workingDirectory string, port int, user, password string, output io.Writer, diagnostic bool, maxOutputTokens int64, tools *ToolsConfigurationSpec, provider *ProviderConfigurationSpec, configContent string) (*Process, error) {
	if !filepath.IsAbs(binary) || safepath.RequireDigest(digest) != nil || port < 1 || port > 65535 {
		return nil, errors.New("invalid OpenCode process configuration")
	}
	client, err := NewClient("http://127.0.0.1:"+strconv.Itoa(port), user, password)
	if err != nil {
		return nil, err
	}
	client.Close()
	root = filepath.Clean(root)
	if err := safepath.Directory(root); err != nil {
		return nil, err
	}
	workingDirectory = filepath.Clean(workingDirectory)
	if err := safepath.Directory(workingDirectory); err != nil {
		return nil, err
	}
	for _, name := range []string{"home", "config", "data", "cache", "state", "tmp"} {
		if err := safepath.Directory(filepath.Join(root, name)); err != nil {
			return nil, err
		}
	}
	f, err := os.Open(binary)
	if err != nil {
		return nil, err
	}
	stat, err := f.Stat()
	if err != nil || !stat.Mode().IsRegular() || stat.Size() > 512<<20 {
		f.Close()
		return nil, errors.New("invalid OpenCode executable")
	}
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(f, (512<<20)+1))
	closeErr := f.Close()
	if err != nil || closeErr != nil || n != stat.Size() || hex.EncodeToString(hash.Sum(nil)) != digest {
		return nil, errors.New("OpenCode executable pin mismatch")
	}
	endpoint := "http://127.0.0.1:" + strconv.Itoa(port)
	authSHA256, err := processServerAuthDigest(user, password)
	if err != nil {
		return nil, err
	}
	launch := processLaunchIdentity{
		ExecutableSHA256: digest, Root: root, WorkingDirectory: workingDirectory, Endpoint: endpoint, AuthSHA256: authSHA256,
		MaxOutputTokens: maxOutputTokens, Diagnostic: diagnostic,
	}
	if tools != nil {
		normalized, normalizeErr := normalizeToolsConfiguration(*tools)
		if normalizeErr != nil {
			return nil, normalizeErr
		}
		bearerSHA256, digestErr := toolsBearerDigest(normalized.bearer)
		if digestErr != nil {
			return nil, digestErr
		}
		launch.Tools = &processToolLaunchIdentity{
			Endpoint: normalized.endpoint, BearerSHA256: bearerSHA256,
			ToolIDs: append([]string(nil), normalized.toolIDs...), TimeoutMillis: normalized.timeoutMillis,
			AllowStructuredOutput: normalized.allowStructuredOutput,
		}
	}
	var env []string
	if provider != nil {
		normalizedProvider, normalizeErr := normalizeProviderConfiguration(*provider)
		if normalizeErr != nil || tools == nil || maxOutputTokens != normalizedProvider.runtimeOutputTokens {
			return nil, errors.New("OpenCode provider process output limit mismatch")
		}
		expectedContent, buildErr := BuildProviderToolsConfiguration(*provider, *tools)
		if buildErr != nil || configContent == "" || configContent != expectedContent {
			return nil, errors.Join(errors.New("invalid OpenCode provider process configuration"), buildErr)
		}
		providerIdentity, identityErr := newProcessProviderLaunchIdentity(*provider, configContent)
		if identityErr != nil {
			return nil, identityErr
		}
		launch.Provider = &providerIdentity
		env, err = hostEnvironment(root, os.Environ(), configContent)
	} else if configContent != "" {
		return nil, errors.New("unexpected OpenCode process configuration content")
	} else if tools == nil {
		env, err = HostEnvironment(root, os.Environ())
	} else {
		env, err = HostEnvironmentWithTools(root, os.Environ(), *tools)
	}
	if err != nil {
		return nil, err
	}
	if maxOutputTokens > 0 {
		env = append(env, "OPENCODE_EXPERIMENTAL_OUTPUT_TOKEN_MAX="+strconv.FormatInt(maxOutputTokens, 10))
	}
	lifetime, cancel := context.WithCancel(ctx)
	args := []string{"serve", "--hostname", "127.0.0.1", "--port", strconv.Itoa(port)}
	if diagnostic {
		args = append(args, "--print-logs", "--log-level", "DEBUG")
	}
	cmd := exec.CommandContext(lifetime, binary, args...)
	cmd.Dir = workingDirectory
	cmd.Env = append(env, "OPENCODE_SERVER_USERNAME="+user, "OPENCODE_SERVER_PASSWORD="+password)
	cmd.Stdout = output
	cmd.Stderr = output
	cmd.WaitDelay = time.Second
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}
	p := &Process{command: cmd, done: make(chan struct{}), cancel: cancel, launch: launch}
	go func() { p.err = cmd.Wait(); close(p.done) }()
	return p, nil
}

// Done closes only after the owned process has been waited on.
func (p *Process) Done() <-chan struct{} { return p.done }

// Wait returns the actual process exit result; cancellation is not success.
func (p *Process) Wait() error { <-p.done; return p.err }

// Close cancels and reaps only the owned root process. It is idempotent.
func (p *Process) Close() error {
	if p == nil {
		return nil
	}
	p.once.Do(p.cancel)
	<-p.done
	return p.err
}

// CloseContext initiates cancellation exactly once and waits only through the
// caller's context. A context error means reaping remains unresolved; Done is
// the authority for whether the owned root process has actually been waited on.
func (p *Process) CloseContext(ctx context.Context) error {
	if p == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("OpenCode process close context required")
	}
	p.once.Do(p.cancel)
	select {
	case <-p.done:
		return p.err
	default:
	}
	select {
	case <-p.done:
		return p.err
	case <-ctx.Done():
		select {
		case <-p.done:
			return p.err
		default:
			return errors.Join(errors.New("OpenCode root process reap incomplete"), ctx.Err())
		}
	}
}

func processServerAuthDigest(user, password string) (string, error) {
	return canonical.Hash("harness.opencode-process-server-auth.v1", struct {
		User     string `json:"user"`
		Password string `json:"password"`
	}{user, password})
}

func toolsBearerDigest(bearer string) (string, error) {
	return canonical.Hash("harness.opencode-process-tools-bearer.v1", struct {
		Bearer string `json:"bearer"`
	}{bearer})
}

func (p *Process) setAdmittedTools(receipt ToolsConfigurationReceipt) error {
	if p == nil {
		return errors.New("invalid OpenCode tools admission transition")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.launch.Tools == nil || p.admittedTools != nil {
		return errors.New("invalid OpenCode tools admission transition")
	}
	launch := p.launch.Tools
	if safepath.RequireDigest(receipt.SHA256) != nil || receipt.MCPServer != ToolsMCPServerName || receipt.Endpoint != launch.Endpoint || receipt.TimeoutMillis != launch.TimeoutMillis || receipt.AllowStructuredOutput != launch.AllowStructuredOutput || !slices.Equal(receipt.ToolIDs, launch.ToolIDs) {
		return errors.New("OpenCode tools admission differs from launch")
	}
	copy := receipt
	copy.ToolIDs = append([]string(nil), receipt.ToolIDs...)
	p.admittedTools = &copy
	return nil
}

func (p *Process) identity() (processLaunchIdentity, bool) {
	if p == nil {
		return processLaunchIdentity{}, false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	if safepath.RequireDigest(p.launch.ExecutableSHA256) != nil || safepath.RequireDigest(p.launch.AuthSHA256) != nil || p.launch.Root == "" || p.launch.WorkingDirectory == "" || p.launch.Endpoint == "" {
		return processLaunchIdentity{}, false
	}
	result := p.launch
	if p.launch.Tools != nil {
		tools := *p.launch.Tools
		tools.ToolIDs = append([]string(nil), p.launch.Tools.ToolIDs...)
		result.Tools = &tools
	}
	if p.launch.Provider != nil {
		provider := *p.launch.Provider
		if p.launch.Provider.ThinkingBudgetTokens != nil {
			budget := *p.launch.Provider.ThinkingBudgetTokens
			provider.ThinkingBudgetTokens = &budget
		}
		result.Provider = &provider
	}
	if p.admittedTools != nil {
		admitted := *p.admittedTools
		admitted.ToolIDs = append([]string(nil), p.admittedTools.ToolIDs...)
		result.AdmittedTools = &admitted
	}
	if p.admittedProvider != nil {
		admitted := *p.admittedProvider
		admitted.ToolsConfiguration.ToolIDs = append([]string(nil), p.admittedProvider.ToolsConfiguration.ToolIDs...)
		if p.admittedProvider.ThinkingBudgetTokens != nil {
			budget := *p.admittedProvider.ThinkingBudgetTokens
			admitted.ThinkingBudgetTokens = &budget
		}
		result.AdmittedProvider = &admitted
	}
	return result, true
}
