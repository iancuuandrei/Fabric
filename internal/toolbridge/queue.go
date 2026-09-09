package toolbridge

import "context"

type callAdmissionState uint8

const (
	callAdmissionQueued callAdmissionState = iota + 1
	callAdmissionRunning
	callAdmissionDone
)

type callAdmissionStatus uint8

const (
	callAdmissionAccepted callAdmissionStatus = iota
	callAdmissionFull
	callAdmissionDuplicate
	callAdmissionClosing
)

type callAdmission struct {
	key   string
	ctx   context.Context
	grant chan struct{}
	state callAdmissionState
}

// admitCall registers a canonical request ID across both queued and executing
// calls. Queue capacity is transport admission only; callbacks are not invoked
// until a running slot is granted.
func (s *Server) admitCall(key string, cancel context.CancelFunc, ctx context.Context) (*callAdmission, callAdmissionStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return nil, callAdmissionClosing
	}
	if s.active[key] != nil {
		return nil, callAdmissionDuplicate
	}
	admission := &callAdmission{key: key, ctx: ctx, grant: make(chan struct{}), state: callAdmissionQueued}
	s.active[key] = cancel
	if s.runningCalls < s.maxConcurrentCalls && len(s.queuedCalls) == 0 {
		admission.state = callAdmissionRunning
		s.runningCalls++
		close(admission.grant)
		return admission, callAdmissionAccepted
	}
	if len(s.queuedCalls) >= s.maxQueuedCalls {
		delete(s.active, key)
		return nil, callAdmissionFull
	}
	s.queuedCalls = append(s.queuedCalls, admission)
	return admission, callAdmissionAccepted
}

func (s *Server) waitForCall(admission *callAdmission) bool {
	select {
	case <-admission.grant:
		if admission.ctx.Err() != nil {
			return false
		}
		s.mu.Lock()
		accepted := !s.closing && admission.state == callAdmissionRunning && admission.ctx.Err() == nil
		s.mu.Unlock()
		return accepted
	case <-admission.ctx.Done():
		return false
	}
}

func (s *Server) finishCall(admission *callAdmission) {
	if admission == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if admission.state == callAdmissionDone {
		return
	}
	delete(s.active, admission.key)
	switch admission.state {
	case callAdmissionRunning:
		if s.runningCalls > 0 {
			s.runningCalls--
		}
	case callAdmissionQueued:
		for index, queued := range s.queuedCalls {
			if queued == admission {
				s.queuedCalls = append(s.queuedCalls[:index], s.queuedCalls[index+1:]...)
				break
			}
		}
	}
	admission.state = callAdmissionDone
	s.grantQueuedLocked()
}

func (s *Server) grantQueuedLocked() {
	if s.closing {
		return
	}
	for s.runningCalls < s.maxConcurrentCalls && len(s.queuedCalls) > 0 {
		admission := s.queuedCalls[0]
		s.queuedCalls = s.queuedCalls[1:]
		if admission.state != callAdmissionQueued {
			continue
		}
		if admission.ctx.Err() != nil {
			admission.state = callAdmissionDone
			delete(s.active, admission.key)
			continue
		}
		admission.state = callAdmissionRunning
		s.runningCalls++
		close(admission.grant)
	}
}
