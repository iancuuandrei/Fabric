package agenttree

import (
	"encoding/json"
	"errors"
	"path"
	"sort"
	"strings"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/safepath"
)

const (
	// MaximumNodes bounds committed nodes plus outstanding reservations.
	MaximumNodes = 256
	// MaximumDepth bounds descendants below the root.
	MaximumDepth = 8
)

const (
	eventCreated   = "agenttree.created"
	eventReserved  = "agenttree.child-reserved"
	eventCommitted = "agenttree.child-committed"
	eventStatus    = "agenttree.status-observed"
	eventMessage   = "agenttree.message-enqueued"
)

// Status is one finite, durable agent lifecycle state.
type Status string

const (
	// StatusQueued means the node is durable but has no active runtime.
	StatusQueued Status = "queued"
	// StatusRunning means a controller observed the admitted runtime start.
	StatusRunning Status = "running"
	// StatusSucceeded means the admitted runtime completed successfully.
	StatusSucceeded Status = "succeeded"
	// StatusFailed means the admitted runtime completed with failure.
	StatusFailed Status = "failed"
	// StatusCanceled means cancellation completed before or during execution.
	StatusCanceled Status = "canceled"
	// StatusUnknown means runtime outcome requires reconciliation.
	StatusUnknown Status = "unknown"
)

// Authority is the repository access boundary assigned to one agent node.
type Authority string

const (
	// AuthorityReadOnly permits observation but no candidate mutation.
	AuthorityReadOnly Authority = "read-only"
	// AuthorityScopedWriter permits mutation only through the controller's
	// already admitted candidate workspace and write policy.
	AuthorityScopedWriter Authority = "scoped-writer"
)

// NodeSpec binds topology to the already admitted invocation and immutable
// context artifact. Name and Role are labels; the derived AgentID is authority.
type NodeSpec struct {
	ParentAgentID string    `json:"parent_agent_id,omitempty"`
	Name          string    `json:"name"`
	Role          string    `json:"role"`
	Authority     Authority `json:"authority"`
	InvocationID  string    `json:"invocation_id"`
	ContextSHA256 string    `json:"context_sha256"`
}

// Node is one committed, immutable tree identity plus its current status.
type Node struct {
	AgentID       string    `json:"agent_id"`
	ParentAgentID string    `json:"parent_agent_id,omitempty"`
	Path          string    `json:"path"`
	Name          string    `json:"name"`
	Role          string    `json:"role"`
	Authority     Authority `json:"authority"`
	InvocationID  string    `json:"invocation_id"`
	ContextSHA256 string    `json:"context_sha256"`
	Depth         int       `json:"depth"`
	Status        Status    `json:"status"`
	ResultSHA256  string    `json:"result_sha256,omitempty"`
}

// Reservation is a crash-visible claim on one child identity and path.
type Reservation struct {
	ReservationID string `json:"reservation_id"`
	Node          Node   `json:"node"`
}

// Snapshot is the fully validated current tree projection.
type Snapshot struct {
	TreeID       string
	RootAgentID  string
	Nodes        []Node
	Reservations []Reservation
	Messages     []Message
}

type createdEvent struct {
	Version int    `json:"version"`
	TreeID  string `json:"tree_id"`
	Root    Node   `json:"root"`
}

type reservationEvent struct {
	Version       int    `json:"version"`
	ReservationID string `json:"reservation_id"`
	Node          Node   `json:"node"`
}

type commitEvent struct {
	Version       int    `json:"version"`
	ReservationID string `json:"reservation_id"`
	AgentID       string `json:"agent_id"`
}

type statusEvent struct {
	Version      int    `json:"version"`
	AgentID      string `json:"agent_id"`
	From         Status `json:"from"`
	To           Status `json:"to"`
	ResultSHA256 string `json:"result_sha256,omitempty"`
}

// Create writes the one root node. TreeID, invocation and context identities
// must already exist; this package does not derive authority from display data.
func Create(journalPath, treeID string, spec NodeSpec) (Node, error) {
	if safepath.RequireDigest(treeID) != nil || spec.ParentAgentID != "" || spec.Name != "root" {
		return Node{}, errors.New("invalid agent tree root")
	}
	root, err := deriveNode(treeID, "", spec, "/root", 0)
	if err != nil {
		return Node{}, err
	}
	payload := createdEvent{Version: 1, TreeID: treeID, Root: root}
	_, err = journal.Append(journalPath, eventCreated, payload, func(events []journal.Event) error {
		_, replayErr := replay(events)
		return replayErr
	})
	return root, err
}

// ReserveChild atomically claims one canonical child path and opaque AgentID.
// A reservation remains visible after a crash and can only be committed by its
// exact ReservationID.
func ReserveChild(journalPath, treeID string, spec NodeSpec) (Reservation, error) {
	state, err := Inspect(journalPath)
	if err != nil {
		return Reservation{}, err
	}
	if state.TreeID != treeID {
		return Reservation{}, errors.New("agent tree identity differs")
	}
	parent, ok := findNode(state.Nodes, spec.ParentAgentID)
	if !ok || parent.Depth >= MaximumDepth {
		return Reservation{}, errors.New("agent tree parent unavailable")
	}
	childPath, err := ChildPath(parent.Path, spec.Name)
	if err != nil {
		return Reservation{}, err
	}
	node, err := deriveNode(treeID, spec.ParentAgentID, spec, childPath, parent.Depth+1)
	if err != nil {
		return Reservation{}, err
	}
	reservationID, err := canonical.Hash("harness.agenttree-reservation.v1", struct {
		TreeID string `json:"tree_id"`
		Node   Node   `json:"node"`
	}{treeID, node})
	if err != nil {
		return Reservation{}, err
	}
	reservation := Reservation{ReservationID: reservationID, Node: node}
	_, err = journal.Append(journalPath, eventReserved, reservationEvent{Version: 1, ReservationID: reservation.ReservationID, Node: reservation.Node}, func(events []journal.Event) error {
		_, replayErr := replay(events)
		return replayErr
	})
	return reservation, err
}

// CommitChild makes an exact prior reservation dispatch-visible.
func CommitChild(journalPath, reservationID string) (Node, error) {
	state, err := Inspect(journalPath)
	if err != nil {
		return Node{}, err
	}
	reservation, ok := findReservation(state.Reservations, reservationID)
	if !ok {
		return Node{}, errors.New("agent child reservation unavailable")
	}
	payload := commitEvent{Version: 1, ReservationID: reservationID, AgentID: reservation.Node.AgentID}
	_, err = journal.Append(journalPath, eventCommitted, payload, func(events []journal.Event) error {
		_, replayErr := replay(events)
		return replayErr
	})
	return reservation.Node, err
}

// ObserveStatus appends one finite lifecycle fact. Running is the only active
// state; terminal observations cannot be reversed or substituted.
func ObserveStatus(journalPath, agentID string, to Status) error {
	return observeStatus(journalPath, agentID, to, "")
}

// ObserveResult durably binds a successful terminal observation to the exact
// accepted result digest.
func ObserveResult(journalPath, agentID, resultSHA256 string) error {
	if safepath.RequireDigest(resultSHA256) != nil {
		return errors.New("invalid agent result identity")
	}
	return observeStatus(journalPath, agentID, StatusSucceeded, resultSHA256)
}

func observeStatus(journalPath, agentID string, to Status, resultSHA256 string) error {
	state, err := Inspect(journalPath)
	if err != nil {
		return err
	}
	node, ok := findNode(state.Nodes, agentID)
	if !ok || !allowedTransition(node.Status, to) {
		return errors.New("invalid agent lifecycle transition")
	}
	if to == StatusSucceeded && safepath.RequireDigest(resultSHA256) != nil || to != StatusSucceeded && resultSHA256 != "" {
		return errors.New("invalid agent result observation")
	}
	payload := statusEvent{Version: 1, AgentID: agentID, From: node.Status, To: to, ResultSHA256: resultSHA256}
	_, err = journal.Append(journalPath, eventStatus, payload, func(events []journal.Event) error {
		_, replayErr := replay(events)
		return replayErr
	})
	return err
}

// Inspect replays the complete durable topology or returns no partial state.
func Inspect(journalPath string) (Snapshot, error) {
	events, err := journal.Read(journalPath)
	if err != nil {
		return Snapshot{}, err
	}
	return replay(events)
}

func replay(events []journal.Event) (Snapshot, error) {
	state := Snapshot{}
	paths := map[string]string{}
	reservations := map[string]Reservation{}
	nodes := map[string]Node{}
	messages := map[string]Message{}
	messageSequences := map[string]int{}
	for index, event := range events {
		switch event.Kind {
		case eventCreated:
			var payload createdEvent
			if index != 0 || decodePayload(event.Payload, &payload) != nil || payload.Version != 1 || safepath.RequireDigest(payload.TreeID) != nil || validateNode(payload.TreeID, payload.Root) != nil || payload.Root.ParentAgentID != "" || payload.Root.Path != "/root" || payload.Root.Depth != 0 || payload.Root.Status != StatusQueued {
				return Snapshot{}, errors.New("invalid agent tree creation")
			}
			state.TreeID, state.RootAgentID = payload.TreeID, payload.Root.AgentID
			nodes[payload.Root.AgentID], paths[payload.Root.Path] = payload.Root, payload.Root.AgentID
		case eventReserved:
			var payload reservationEvent
			decodeErr := decodePayload(event.Payload, &payload)
			identityErr := safepath.RequireDigest(payload.ReservationID)
			nodeErr := validateNode(state.TreeID, payload.Node)
			if state.TreeID == "" || decodeErr != nil || payload.Version != 1 || identityErr != nil || nodeErr != nil || len(nodes)+len(reservations) >= MaximumNodes {
				return Snapshot{}, errors.Join(errors.New("invalid agent child reservation"), decodeErr, identityErr, nodeErr)
			}
			parent, ok := nodes[payload.Node.ParentAgentID]
			if !ok || payload.Node.Depth != parent.Depth+1 || payload.Node.Depth > MaximumDepth {
				return Snapshot{}, errors.New("invalid agent child parent")
			}
			wantPath, pathErr := ChildPath(parent.Path, payload.Node.Name)
			wantID, idErr := nodeID(state.TreeID, payload.Node.ParentAgentID, payload.Node.Name, payload.Node.Role, payload.Node.Authority, payload.Node.InvocationID, payload.Node.ContextSHA256)
			wantReservation, reservationErr := canonical.Hash("harness.agenttree-reservation.v1", struct {
				TreeID string `json:"tree_id"`
				Node   Node   `json:"node"`
			}{state.TreeID, payload.Node})
			if pathErr != nil || idErr != nil || reservationErr != nil || payload.Node.Path != wantPath || payload.Node.AgentID != wantID || payload.ReservationID != wantReservation || paths[payload.Node.Path] != "" || nodes[payload.Node.AgentID].AgentID != "" || reservations[payload.ReservationID].ReservationID != "" {
				return Snapshot{}, errors.New("agent child identity already reserved")
			}
			reservations[payload.ReservationID] = Reservation{ReservationID: payload.ReservationID, Node: payload.Node}
			paths[payload.Node.Path] = payload.Node.AgentID
		case eventCommitted:
			var payload commitEvent
			if decodePayload(event.Payload, &payload) != nil || payload.Version != 1 {
				return Snapshot{}, errors.New("invalid agent child commit")
			}
			reservation, ok := reservations[payload.ReservationID]
			if !ok || reservation.Node.AgentID != payload.AgentID {
				return Snapshot{}, errors.New("agent child reservation differs")
			}
			nodes[payload.AgentID] = reservation.Node
			delete(reservations, payload.ReservationID)
		case eventStatus:
			var payload statusEvent
			if decodeErr := decodePayload(event.Payload, &payload); decodeErr != nil || payload.Version != 1 {
				return Snapshot{}, errors.Join(errors.New("invalid agent status observation"), decodeErr)
			}
			node, ok := nodes[payload.AgentID]
			validResult := payload.To == StatusSucceeded && safepath.RequireDigest(payload.ResultSHA256) == nil || payload.To != StatusSucceeded && payload.ResultSHA256 == ""
			if !ok || node.Status != payload.From || !allowedTransition(payload.From, payload.To) || !validResult {
				return Snapshot{}, errors.New("agent status history differs")
			}
			node.Status = payload.To
			node.ResultSHA256 = payload.ResultSHA256
			nodes[payload.AgentID] = node
		case eventMessage:
			var payload messageEvent
			if decodeErr := decodePayload(event.Payload, &payload); decodeErr != nil || payload.Version != 1 {
				return Snapshot{}, errors.Join(errors.New("invalid agent message"), decodeErr)
			}
			message := payload.Message
			from, fromOK := nodes[message.FromAgentID]
			to, toOK := nodes[message.ToAgentID]
			if !fromOK || !toOK || !messageEndpointsAvailable(from.Status, to.Status, message.Wake) || safepath.RequireDigest(message.MessageID) != nil || safepath.RequireDigest(message.BodySHA256) != nil || message.Sequence != messageSequences[message.ToAgentID]+1 || messages[message.MessageID].MessageID != "" || len(messages) >= MaximumMessages {
				return Snapshot{}, errors.New("invalid agent message identity or sequence")
			}
			messages[message.MessageID] = message
			messageSequences[message.ToAgentID] = message.Sequence
		default:
			return Snapshot{}, errors.New("unexpected agent tree event")
		}
	}
	if len(events) > 0 && state.TreeID == "" {
		return Snapshot{}, errors.New("agent tree creation missing")
	}
	for _, node := range nodes {
		state.Nodes = append(state.Nodes, node)
	}
	for _, reservation := range reservations {
		state.Reservations = append(state.Reservations, reservation)
	}
	for _, message := range messages {
		state.Messages = append(state.Messages, message)
	}
	sort.Slice(state.Nodes, func(i, j int) bool { return state.Nodes[i].Path < state.Nodes[j].Path })
	sort.Slice(state.Reservations, func(i, j int) bool { return state.Reservations[i].Node.Path < state.Reservations[j].Node.Path })
	sort.Slice(state.Messages, func(i, j int) bool {
		if state.Messages[i].ToAgentID != state.Messages[j].ToAgentID {
			return state.Messages[i].ToAgentID < state.Messages[j].ToAgentID
		}
		return state.Messages[i].Sequence < state.Messages[j].Sequence
	})
	return state, nil
}

// ChildPath returns one canonical display path beneath parent.
func ChildPath(parent, name string) (string, error) {
	if !canonicalAgentPath(parent) || !validLabel(name) {
		return "", errors.New("invalid canonical agent path")
	}
	return parent + "/" + name, nil
}

// ResolveTarget resolves one absolute or direct-child display target.
func ResolveTarget(current, target string) (string, error) {
	if target == "/root" || strings.HasPrefix(target, "/root/") {
		if !canonicalAgentPath(target) {
			return "", errors.New("invalid canonical agent target")
		}
		return target, nil
	}
	if !validLabel(target) || !canonicalAgentPath(current) {
		return "", errors.New("invalid relative agent target")
	}
	return ChildPath(current, target)
}

func canonicalAgentPath(value string) bool {
	if value != "/root" && !strings.HasPrefix(value, "/root/") || path.Clean(value) != value {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(value, "/"), "/")
	if len(parts) == 0 || parts[0] != "root" || len(parts)-1 > MaximumDepth {
		return false
	}
	for _, part := range parts {
		if !validLabel(part) {
			return false
		}
	}
	return true
}

func deriveNode(treeID, parentID string, spec NodeSpec, nodePath string, depth int) (Node, error) {
	pathParts := strings.Split(strings.TrimPrefix(nodePath, "/"), "/")
	if spec.ParentAgentID != parentID || !validLabel(spec.Name) || !validLabel(spec.Role) || !validAuthority(spec.Authority) || !canonicalAgentPath(nodePath) || depth < 0 || depth > MaximumDepth || len(pathParts) != depth+1 || pathParts[len(pathParts)-1] != spec.Name || depth == 0 && parentID != "" || depth > 0 && safepath.RequireDigest(parentID) != nil || safepath.RequireDigest(spec.InvocationID) != nil || safepath.RequireDigest(spec.ContextSHA256) != nil {
		return Node{}, errors.New("invalid agent node specification")
	}
	id, err := nodeID(treeID, parentID, spec.Name, spec.Role, spec.Authority, spec.InvocationID, spec.ContextSHA256)
	if err != nil {
		return Node{}, err
	}
	return Node{AgentID: id, ParentAgentID: parentID, Path: nodePath, Name: spec.Name, Role: spec.Role, Authority: spec.Authority, InvocationID: spec.InvocationID, ContextSHA256: spec.ContextSHA256, Depth: depth, Status: StatusQueued}, nil
}

func nodeID(treeID, parentID, name, role string, authority Authority, invocationID, contextSHA256 string) (string, error) {
	return canonical.Hash("harness.agenttree-node.v1", struct {
		TreeID        string    `json:"tree_id"`
		ParentAgentID string    `json:"parent_agent_id,omitempty"`
		Name          string    `json:"name"`
		Role          string    `json:"role"`
		Authority     Authority `json:"authority"`
		InvocationID  string    `json:"invocation_id"`
		ContextSHA256 string    `json:"context_sha256"`
	}{treeID, parentID, name, role, authority, invocationID, contextSHA256})
}

func validateNode(treeID string, node Node) error {
	want, err := deriveNode(treeID, node.ParentAgentID, NodeSpec{ParentAgentID: node.ParentAgentID, Name: node.Name, Role: node.Role, Authority: node.Authority, InvocationID: node.InvocationID, ContextSHA256: node.ContextSHA256}, node.Path, node.Depth)
	if err != nil || want != node {
		return errors.New("invalid agent node")
	}
	return nil
}

// ValidateQueuedNode verifies a newly admitted node's opaque identity,
// canonical path, immutable context and authority.
func ValidateQueuedNode(treeID string, node Node) error {
	return validateNode(treeID, node)
}

func validAuthority(authority Authority) bool {
	return authority == AuthorityReadOnly || authority == AuthorityScopedWriter
}

func validLabel(value string) bool {
	if len(value) < 1 || len(value) > 63 || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-') {
			return false
		}
	}
	return true
}

func allowedTransition(from, to Status) bool {
	return from == StatusQueued && (to == StatusRunning || to == StatusCanceled) ||
		from == StatusRunning && (to == StatusSucceeded || to == StatusFailed || to == StatusCanceled || to == StatusUnknown) ||
		from == StatusUnknown && to == StatusSucceeded
}

func findNode(nodes []Node, id string) (Node, bool) {
	for _, node := range nodes {
		if node.AgentID == id {
			return node, true
		}
	}
	return Node{}, false
}

func findReservation(reservations []Reservation, id string) (Reservation, bool) {
	for _, reservation := range reservations {
		if reservation.ReservationID == id {
			return reservation, true
		}
	}
	return Reservation{}, false
}

func decodePayload(raw json.RawMessage, target any) error {
	return canonical.Decode(raw, target)
}
