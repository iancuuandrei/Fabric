package control

import "harness.local/engorch/internal/canonical"

// agentToolCallerIdentity binds transport receipts to the durable admission and
// scheduler claim. Paths and the transient recovery flag are deliberately not
// identities: the validated journal contents supply the authority. Historical
// verification remains possible after the caller has completed.
func agentToolCallerIdentity(controllerPath, schedulerPath string, binding agentDispatchBinding) (string, error) {
	verifier, err := newAgentToolReceiptVerifier(controllerPath, schedulerPath, binding)
	if err != nil {
		return "", err
	}
	return canonical.Hash("harness.agent-tool-caller.v1", struct {
		Version        int    `json:"version"`
		AdmissionID    string `json:"admission_id"`
		InvocationID   string `json:"invocation_id"`
		ScheduleID     string `json:"schedule_id"`
		ClaimID        string `json:"claim_id"`
		ControllerHead string `json:"claim_controller_head"`
	}{1, binding.AdmissionID, binding.InvocationID, verifier.scheduleID, verifier.claimID, verifier.controllerHead})
}
