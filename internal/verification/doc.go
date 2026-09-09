// Package verification owns bounded direct-argv check invocation and receipts.
// It does not authorize checks or promote run state; the controller must persist
// intent and bracket execution with candidate observations. See
// docs/specifications/verification.md for environment and termination limits.
package verification
