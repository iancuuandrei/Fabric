package opencoderuntime

import (
	"context"
	"errors"
	"time"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/contextbroker"
	"harness.local/engorch/internal/contextmcp"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/opencode"
	"harness.local/engorch/internal/providerproxy"
)

type interruptShutdownLive struct {
	intent InterruptShutdownIntent
	open   contextbroker.State
}

func closeRuntimeResourcesAfterInterrupt(cfg ExecuteConfig, prepared *preparedExecution, process *opencode.Process, client *opencode.Client, contextRunning *contextmcp.OwnedRunning, proxyRunning *providerproxy.Running) error {
	if cfg.InterruptShutdown == nil {
		closeRuntimeResources(process, client, contextRunning, proxyRunning, cfg.Broker, cfg.SealTimeout)
		return nil
	}
	expected, requested, lookupErr := cfg.InterruptShutdown(cfg.Intent)
	if lookupErr != nil || !requested {
		closeRuntimeResources(process, client, contextRunning, proxyRunning, cfg.Broker, cfg.SealTimeout)
		return lookupErr
	}
	live, err := prepareInterruptShutdown(cfg, prepared, expected, process, contextRunning, proxyRunning)
	if err != nil {
		closeRuntimeResources(process, client, contextRunning, proxyRunning, cfg.Broker, cfg.SealTimeout)
		return err
	}
	proof, shutdownErr := verifyOwnedInterruptShutdown(cfg, live, process, client, contextRunning, proxyRunning)
	if shutdownErr != nil {
		return shutdownErr
	}
	_, err = recordInterruptShutdownReceipt(cfg.RuntimePath, live.intent, proof)
	return err
}

func prepareInterruptShutdown(cfg ExecuteConfig, prepared *preparedExecution, expected InterruptShutdownExpected, process *opencode.Process, contextRunning *contextmcp.OwnedRunning, proxyRunning *providerproxy.Running) (interruptShutdownLive, error) {
	var zero interruptShutdownLive
	if prepared == nil || process == nil || contextRunning == nil || proxyRunning == nil || cfg.Broker == nil || !equalCanonical(expected.Runtime, cfg.Intent) {
		return zero, errors.New("complete owned interrupt shutdown resources required")
	}
	if err := validateInterruptShutdownExpected(expected); err != nil {
		return zero, err
	}
	state, err := inspectRuntimeJournal(cfg.RuntimePath)
	if err != nil || state.Intent == nil || state.Bound == nil || !equalCanonical(*state.Intent, normalizedIntent(cfg.Intent)) || state.Bound.SealExpected.Provider == nil {
		return zero, errors.Join(errors.New("interrupt shutdown requires exact bound runtime"), err)
	}
	if err := validateInterruptShutdownMCPOwner(cfg, contextRunning, prepared.mcpBearer); err != nil {
		return zero, err
	}
	if err := proxyRunning.ValidateOwner(cfg.Transport, cfg.Credential, prepared.proxyBearer); err != nil {
		return zero, err
	}
	processIdentity, err := process.ProviderIdentity()
	if err != nil || !equalCanonical(processIdentity, state.Bound.SealExpected.Provider.Process) {
		return zero, errors.Join(errors.New("interrupt shutdown process identity mismatch"), err)
	}
	proxyIdentity, err := proxyRunning.ProviderSealIdentity(prepared.proxyBearer)
	if err != nil || !equalCanonical(proxyIdentity, state.Bound.SealExpected.Provider.Proxy) {
		return zero, errors.Join(errors.New("interrupt shutdown proxy identity mismatch"), err)
	}
	pid, err := process.ProcessID()
	if err != nil {
		return zero, err
	}
	open, err := contextbroker.Inspect(cfg.Paths.Broker)
	if err != nil || open.Binding == nil || open.Closed {
		return zero, errors.Join(errors.New("interrupt shutdown broker is not open"), err)
	}
	bindingID, err := open.Binding.ID()
	if err != nil {
		return zero, err
	}
	openID, err := interruptBrokerStateID(open)
	if err != nil {
		return zero, err
	}
	openHead, err := journalHead(cfg.Paths.Broker)
	if err != nil {
		return zero, err
	}
	mcpID, err := canonical.Hash("harness.opencode-runtime-interrupt-mcp-server.v1", struct {
		Endpoint      string `json:"endpoint"`
		CatalogSHA256 string `json:"catalog_sha256"`
		BrokerID      string `json:"broker_id"`
	}{contextRunning.URL(), contextRunning.CatalogHash(), bindingID})
	if err != nil || contextRunning.URL() != state.Bound.SealExpected.Tools.Endpoint || contextRunning.CatalogHash() != state.Bound.Session.Binding.CatalogSHA256 {
		return zero, errors.Join(errors.New("interrupt shutdown MCP identity mismatch"), err)
	}
	resources := InterruptShutdownResources{
		Version: 1, ProcessID: pid, ProviderProcessSHA256: processIdentity.SHA256,
		ProviderProxySHA256: proxyIdentity.SHA256, MCPServerSHA256: mcpID,
		BrokerPath: cfg.Paths.Broker, BrokerBindingID: bindingID, BrokerOpenStateID: openID, BrokerOpenHead: openHead,
	}
	intent, err := recordInterruptShutdownIntent(cfg.RuntimePath, expected, resources)
	if err != nil {
		return zero, err
	}
	return interruptShutdownLive{intent: intent, open: open}, nil
}

func validateInterruptShutdownMCPOwner(cfg ExecuteConfig, running *contextmcp.OwnedRunning, bearer string) error {
	return validateExecutionMCPOwner(cfg, running, bearer)
}

func verifyOwnedInterruptShutdown(cfg ExecuteConfig, live interruptShutdownLive, process *opencode.Process, client *opencode.Client, contextRunning *contextmcp.OwnedRunning, proxyRunning *providerproxy.Running) (verifiedInterruptShutdown, error) {
	var zero verifiedInterruptShutdown
	timeout := cfg.SealTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var shutdownErr error
	// Initiate cancellation on the exact owned root before waiting for local
	// handlers. OpenCode owns the client side of any in-flight proxy request;
	// waiting for that handler first could otherwise consume the deadline.
	kick, stopRoot := context.WithCancel(context.Background())
	stopRoot()
	_ = process.CloseContext(kick)
	if err := contextRunning.Close(ctx); err != nil {
		shutdownErr = errors.Join(shutdownErr, errors.New("interrupt MCP shutdown failed"), err)
	}
	if err := waitInterruptLifecycle(ctx, contextRunning.Wait); err != nil {
		shutdownErr = errors.Join(shutdownErr, errors.New("interrupt MCP handlers remain unsettled"), err)
	}
	if err := proxyRunning.Close(ctx); err != nil {
		shutdownErr = errors.Join(shutdownErr, errors.New("interrupt provider proxy shutdown failed"), err)
	}
	if err := waitInterruptLifecycle(ctx, proxyRunning.Wait); err != nil {
		shutdownErr = errors.Join(shutdownErr, errors.New("interrupt provider handlers remain unsettled"), err)
	}
	closeErr := process.CloseContext(ctx)
	select {
	case <-process.Done():
	default:
		shutdownErr = errors.Join(shutdownErr, errors.New("interrupt OpenCode root process remains unreaped"), closeErr)
	}
	waitErr := error(nil)
	select {
	case <-process.Done():
		waitErr = process.Wait()
	default:
	}
	if closeErr != nil && (waitErr == nil || closeErr.Error() != waitErr.Error()) {
		shutdownErr = errors.Join(shutdownErr, errors.New("interrupt OpenCode process wait identity differs"), closeErr, waitErr)
	}
	if client != nil {
		client.Close()
	}
	current, settledID, settledHead, settleErr := settledInterruptBroker(cfg.Paths.Broker, live.open, live.intent.Resources.BrokerOpenHead)
	shutdownErr = errors.Join(shutdownErr, settleErr)
	if err := cfg.Broker.Close(); err != nil {
		shutdownErr = errors.Join(shutdownErr, errors.New("interrupt broker closure failed"), err)
	}
	closed, err := contextbroker.Inspect(cfg.Paths.Broker)
	wantClosed := current
	wantClosed.Closed = true
	if err != nil || !equalCanonical(closed, wantClosed) {
		shutdownErr = errors.Join(shutdownErr, errors.New("interrupt closed broker state mismatch"), err)
	}
	if shutdownErr != nil {
		return zero, shutdownErr
	}
	closedID, err := interruptBrokerStateID(closed)
	if err != nil {
		return zero, err
	}
	closedHead, err := journalHead(cfg.Paths.Broker)
	if err != nil {
		return zero, err
	}
	runtimeEvents, err := journal.Read(cfg.RuntimePath)
	if err != nil || len(runtimeEvents) == 0 {
		return zero, errors.Join(errors.New("interrupt shutdown runtime head unavailable"), err)
	}
	waitErrorID := ""
	if waitErr != nil {
		waitErrorID, err = canonical.Hash("harness.opencode-runtime-interrupt-process-wait.v1", struct {
			Error string `json:"error"`
		}{waitErr.Error()})
		if err != nil {
			return zero, err
		}
	}
	resources := live.intent.Resources
	receipt := InterruptShutdownReceipt{
		Version: 1, IntentID: live.intent.ID, RuntimeHead: runtimeEvents[len(runtimeEvents)-1].Hash,
		ProcessID: resources.ProcessID, ProviderProcessSHA256: resources.ProviderProcessSHA256,
		ProviderProxySHA256: resources.ProviderProxySHA256, MCPServerSHA256: resources.MCPServerSHA256,
		BrokerBindingID: resources.BrokerBindingID, BrokerOpenStateID: resources.BrokerOpenStateID,
		BrokerOpenHead: resources.BrokerOpenHead, BrokerSettledStateID: settledID, BrokerSettledHead: settledHead,
		BrokerClosedStateID: closedID, BrokerClosedHead: closedHead,
		MCPHandlersStopped: true, ProviderProxyStopped: true, ProviderHandlersSettled: true,
		RootProcessReaped: true, ProcessWaitErrorSHA256: waitErrorID, BrokerClosed: true,
	}
	return verifiedInterruptShutdown{seal: &interruptShutdownVerificationSeal{}, receipt: receipt}, nil
}

func settledInterruptBroker(path string, open contextbroker.State, openHead string) (contextbroker.State, string, string, error) {
	var zero contextbroker.State
	brokerEvents, eventsErr := journal.Read(path)
	current, inspectErr := contextbroker.Inspect(path)
	if eventsErr != nil || inspectErr != nil || eventIndex(brokerEvents, openHead) < 0 || current.Binding == nil || open.Binding == nil || !equalCanonical(*current.Binding, *open.Binding) || current.Closed || current.Pending != nil {
		return zero, "", "", errors.Join(errors.New("interrupt broker did not settle from its admitted prefix"), eventsErr, inspectErr)
	}
	settledID, err := interruptBrokerStateID(current)
	if err != nil {
		return zero, "", "", err
	}
	settledHead, err := journalHead(path)
	if err != nil {
		return zero, "", "", err
	}
	return current, settledID, settledHead, nil
}

func waitInterruptLifecycle(ctx context.Context, wait func() error) error {
	done := make(chan error, 1)
	go func() { done <- wait() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func interruptBrokerStateID(state contextbroker.State) (string, error) {
	return canonical.Hash("harness.opencode-runtime-interrupt-broker-state.v1", state)
}
