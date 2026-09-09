// Package contextbroker journals invocation-bound repository context requests.
// Callers supply admitted source or candidate identity and retain any workspace
// lease; broker construction does not grant model or filesystem authority.
package contextbroker
