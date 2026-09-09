package toolbridge

// CatalogIdentity validates a catalog at the transport's maximum body bound
// and returns the same identity used by Server.CatalogHash. A server may impose
// a smaller response limit; this helper grants no listener or tool authority.
func CatalogIdentity(catalog []ToolDefinition) (string, error) {
	_, _, identity, err := validateCatalog(catalog, maximumBodyBound)
	return identity, err
}

// CatalogHash identifies the immutable catalog served by this exact listener.
// It does not establish any controller invocation or provider authority.
func (r *Running) CatalogHash() string {
	if r == nil || r.owner == nil {
		return ""
	}
	return r.owner.CatalogHash()
}
