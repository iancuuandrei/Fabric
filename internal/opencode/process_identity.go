package opencode

import "errors"

// ProcessID returns the positive operating-system identifier of the exact
// owned process. It performs no process lookup and grants no signal authority.
func (p *Process) ProcessID() (int, error) {
	if p == nil || p.command == nil || p.command.Process == nil {
		return 0, errors.New("owned OpenCode process required")
	}
	if _, ok := p.identity(); !ok || p.command.Process.Pid <= 0 {
		return 0, errors.New("invalid owned OpenCode process identity")
	}
	return p.command.Process.Pid, nil
}
