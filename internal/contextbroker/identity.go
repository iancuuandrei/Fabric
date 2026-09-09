package contextbroker

// JournalPath identifies the journal selected at Open. The caller must retain
// its exclusive invocation ownership; a path is not a cross-process lease.
func (b *Broker) JournalPath() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.path
}

// BindingID returns the immutable binding identity selected at Open.
func (b *Broker) BindingID() (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.binding.ID()
}
