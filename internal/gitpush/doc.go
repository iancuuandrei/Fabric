// Package gitpush binds remote-ref publication to exact inputs and authorization.
// It observes remote refs and executes pushes under a caller-held lease and
// persisted intent. Controller receipt admission remains a separate boundary.
package gitpush
