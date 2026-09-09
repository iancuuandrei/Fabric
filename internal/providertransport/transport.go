// Package providertransport performs controller-owned, single-attempt provider
// POSTs after durable admission. It never discovers endpoints or credentials.
package providertransport

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"reflect"
	"time"

	"harness.local/engorch/internal/access"
	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/providercredential"
	"harness.local/engorch/internal/providergateway"
)

const (
	maximumExecutionWindow = 30 * time.Minute
	maximumHeaderBytes     = 64 << 10
	defaultUserAgent       = "engorch-providertransport/1"
)

var (
	// ErrRejected means no provider call intent was durably admitted.
	ErrRejected = errors.New("provider transport request rejected")
	// ErrPending means a durable intent exists or journal state cannot prove its
	// absence, so the external effect must be treated as unknown. The same call
	// must never be sent again.
	ErrPending = errors.New("provider transport call outcome unresolved")
)

// ClientOptions fixes the TLS roots used by the transport. Nil selects a
// snapshot of the host system roots. No proxy, redirect or custom round tripper
// can be injected.
type ClientOptions struct {
	RootCAs *x509.CertPool
}

// Client retains only a private TLS-root snapshot.
type Client struct {
	rootCAs *x509.CertPool
}

// Request binds one exact body to an admitted invocation, gateway journal and
// short-lived credential lease. Expectation is interpreted only by the adapter
// named in Binding.
type Request struct {
	AccessJournalPath  string
	GatewayJournalPath string
	Policy             access.Policy
	Intent             access.Intent
	Binding            providergateway.Binding
	Lease              *providercredential.Lease
	Body               []byte
	Expectation        providergateway.AdapterRequestExpectation
}

// Result is returned only after the complete response has been validated and
// durably recorded. Body is an owned copy suitable for returning to OpenCode.
type Result struct {
	Body    []byte
	Receipt providergateway.CallReceipt
	Content *providergateway.ProviderResponseContent
}

// NewClient freezes the selected trust roots.
func NewClient(options ClientOptions) (*Client, error) {
	roots := options.RootCAs
	if roots == nil {
		var err error
		roots, err = x509.SystemCertPool()
		if err != nil {
			return nil, ErrRejected
		}
	}
	return &Client{rootCAs: roots.Clone()}, nil
}

// Execute performs at most one provider POST. Every error after Begin leaves a
// pending gateway call and returns ErrPending with no response bytes.
func (c *Client) Execute(ctx context.Context, input Request) (Result, error) {
	if err := validateExecutionContext(ctx); err != nil || c == nil || c.rootCAs == nil || input.Lease == nil {
		return Result{}, rejected(ctx)
	}
	request, err := freezeRequest(input)
	if err != nil || validateLeaseBinding(request) != nil || validateReservation(request) != nil {
		return Result{}, rejected(ctx)
	}
	metadata, err := providergateway.ValidateAdapterRequest(request.Body, request.Binding, request.Expectation)
	if errors.Is(err, providergateway.ErrCapabilityUnavailable) {
		if !tryRecordPreflightRejection(request, preflightValidatePhase, err) {
			return Result{}, pending(ctx)
		}
		return Result{}, err
	}
	if err != nil {
		if !tryRecordPreflightRejection(request, preflightValidatePhase, err) {
			return Result{}, pending(ctx)
		}
		return Result{}, rejected(ctx)
	}
	if metadata.SHA256 == "" || metadata.SizeBytes != int64(len(request.Body)) || metadata.Model != request.Binding.Model.Model || metadata.MaxOutputTokens != request.Expectation.MaxOutputTokens {
		if !tryRecordPreflightRejection(request, preflightValidatePhase, errors.New("provider request metadata differs from bound expectation")) {
			return Result{}, pending(ctx)
		}
		return Result{}, rejected(ctx)
	}
	beforeBegin, err := providergateway.Inspect(request.GatewayJournalPath)
	if err != nil {
		return Result{}, pending(ctx)
	}
	call, err := providergateway.BeginWithExpectation(request.GatewayJournalPath, request.AccessJournalPath, request.Policy, request.Intent, request.Binding, metadata.SHA256, metadata.SizeBytes, request.Expectation)
	if err != nil {
		afterBegin, inspectErr := providergateway.Inspect(request.GatewayJournalPath)
		if inspectErr != nil || afterBegin.Pending != nil || !reflect.DeepEqual(beforeBegin, afterBegin) {
			return Result{}, pending(ctx)
		}
		if !tryRecordPreflightRejectionAtState(request, preflightBeginPhase, err, beforeBegin) {
			return Result{}, pending(ctx)
		}
		return Result{}, rejected(ctx)
	}

	raw, evidence, err := c.postOnce(ctx, request, call)
	if err != nil {
		return Result{}, recordAttemptFailure(ctx, request, call, err)
	}
	maximumTotal := request.Binding.Model.ContextWindowTokens + metadata.MaxOutputTokens
	response, err := providergateway.DecodeAdapterResponse(raw, request.Binding, providergateway.AdapterResponseExpectation{
		MaxBytes:                 int(request.Binding.Model.MaxResponseBytes),
		MaxTotalTokens:           maximumTotal,
		RequiredCapabilities:     request.Expectation.RequiredCapabilities,
		ResponseFraming:          request.Expectation.ResponseFraming,
		TerminalStructuredOutput: request.Expectation.TerminalStructuredOutput,
	})
	if err != nil || response.SHA256 == "" || response.SizeBytes != int64(len(raw)) || !providergateway.AcceptsObservedModel(request.Binding.Model, response.ObservedModel) || response.Status != "completed" || !response.UsageComplete || response.Usage.OutputTokens > call.MaxOutputTokens || (response.Finish != "stop" && response.Finish != "tool_calls") {
		failure := observedDecodeFailure(raw)
		if err == nil {
			switch {
			case response.SHA256 == "":
				err = errors.New("missing required response SHA256")
			case response.SizeBytes != int64(len(raw)):
				err = errors.New("response SizeBytes differs from captured bytes")
			case !providergateway.AcceptsObservedModel(request.Binding.Model, response.ObservedModel):
				err = errors.New("response ObservedModel differs from authorized model")
			case response.Status != "completed":
				err = errors.New("response Status is not completed")
			case !response.UsageComplete:
				err = errors.New("response UsageComplete is false")
			case response.Usage.OutputTokens > call.MaxOutputTokens:
				err = errors.New("response output_tokens exceeds provider output limit")
			default:
				err = errors.New("response Finish is neither stop nor tool_calls")
			}
		}
		if evidenceErr := attachDecodeEvidence(failure, evidence, classifyDecodeFailure(raw, request.Expectation.ResponseFraming, err), "adapter", err); evidenceErr != nil {
			return Result{}, pending(ctx)
		}
		var terminal *providergateway.AnthropicTerminalError
		if request.Binding.Model.AdapterID == providergateway.AnthropicMessagesAdapter && errors.As(err, &terminal) && terminal != nil && terminal.Status == "refusal" && response.Status == "refusal" && response.UsageComplete && response.Usage.OutputTokens <= call.MaxOutputTokens && providergateway.AcceptsObservedModel(request.Binding.Model, response.ObservedModel) {
			failure.observation.Class = providergateway.FailureContentPolicy
		}
		return Result{}, recordAttemptFailure(ctx, request, call, failure)
	}
	receipt := providergateway.CallReceipt{
		Version:        1,
		BindingID:      call.BindingID,
		InvocationID:   call.InvocationID,
		CallID:         call.CallID,
		ResponseSHA256: response.SHA256,
		ResponseBytes:  response.SizeBytes,
		ResponseID:     response.ResponseID,
		ObservedModel:  response.ObservedModel,
		Finish:         response.Finish,
		// StreamComplete is the legacy receipt field for whole-response
		// completeness; a fully buffered JSON response satisfies it as well.
		StreamComplete:       true,
		UsageComplete:        response.UsageComplete,
		Usage:                cloneUsage(response.Usage),
		Semantic:             cloneSemantic(response.Semantic),
		RequestExpectationID: call.RequestExpectationID,
	}
	if err := providergateway.Complete(request.GatewayJournalPath, request.AccessJournalPath, request.Policy, request.Intent, request.Binding, call, receipt); err != nil {
		return Result{}, pending(ctx)
	}
	return Result{Body: append([]byte(nil), raw...), Receipt: receipt, Content: cloneContent(response.Content)}, nil
}

func validateExecutionContext(ctx context.Context) error {
	if ctx == nil {
		return ErrRejected
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > maximumExecutionWindow {
		return ErrRejected
	}
	return nil
}

func freezeRequest(source Request) (Request, error) {
	var result Request
	if source.AccessJournalPath == "" || source.GatewayJournalPath == "" || len(source.Body) == 0 {
		return result, ErrRejected
	}
	result.AccessJournalPath = source.AccessJournalPath
	result.GatewayJournalPath = source.GatewayJournalPath
	result.Lease = source.Lease
	result.Body = append([]byte(nil), source.Body...)
	result.Expectation = cloneExpectation(source.Expectation)
	for _, pair := range []struct {
		source any
		target any
	}{{source.Policy, &result.Policy}, {source.Intent, &result.Intent}, {source.Binding, &result.Binding}} {
		raw, err := canonical.Bytes(pair.source)
		if err != nil || canonical.Decode(raw, pair.target) != nil {
			return Request{}, ErrRejected
		}
	}
	return result, nil
}

func cloneExpectation(source providergateway.AdapterRequestExpectation) providergateway.AdapterRequestExpectation {
	result := source
	result.Controls = append([]byte(nil), source.Controls...)
	result.Tools = append([]providergateway.RequestTool(nil), source.Tools...)
	for index := range result.Tools {
		result.Tools[index].Parameters = append([]byte(nil), source.Tools[index].Parameters...)
	}
	if source.RequiredCapabilities != nil {
		required := *source.RequiredCapabilities
		required.Schema = append([]byte(nil), source.RequiredCapabilities.Schema...)
		result.RequiredCapabilities = &required
	}
	if source.TerminalStructuredOutput != nil {
		terminal := *source.TerminalStructuredOutput
		terminal.Schema = append([]byte(nil), source.TerminalStructuredOutput.Schema...)
		result.TerminalStructuredOutput = &terminal
	}
	return result
}

func cloneContent(source *providergateway.ProviderResponseContent) *providergateway.ProviderResponseContent {
	if source == nil {
		return nil
	}
	result := *source
	if source.ToolCalls != nil {
		result.ToolCalls = make([]providergateway.ProviderToolCallContent, len(source.ToolCalls))
		for index, call := range source.ToolCalls {
			result.ToolCalls[index] = call
			result.ToolCalls[index].Arguments = append([]byte(nil), call.Arguments...)
		}
	}
	if source.Reasoning != nil {
		raw, err := canonical.Bytes(source.Reasoning)
		if err != nil {
			return nil
		}
		var reasoning providergateway.ProviderReasoningContent
		if canonical.Decode(raw, &reasoning) != nil {
			return nil
		}
		result.Reasoning = &reasoning
	}
	return &result
}

func validateLeaseBinding(request Request) error {
	lease := request.Lease.Binding()
	if _, err := lease.ID(); err != nil || request.Binding.Endpoint.Auth == nil {
		return ErrRejected
	}
	expected := providercredential.Binding{
		Version:            1,
		AccessPolicyID:     request.Binding.AccessPolicyID,
		AccessInvocationID: request.Binding.AccessInvocationID,
		RouteID:            request.Binding.RouteID,
		AccessProfileID:    request.Intent.Route.AccessID,
		Provider:           request.Binding.Endpoint.Provider,
		Runtime:            request.Intent.Route.Runtime,
		EndpointID:         request.Binding.EndpointID,
		CredentialRef:      request.Binding.Endpoint.Auth.CredentialRef,
		AuthScheme:         request.Binding.Endpoint.Auth.Scheme,
		BillingMode:        request.Intent.Reservation.BillingMode,
	}
	if _, err := expected.ID(); err != nil || !reflect.DeepEqual(lease, expected) {
		return ErrRejected
	}
	return nil
}

func validateReservation(request Request) error {
	expected, err := providergateway.BindingForAccess(request.Policy, request.Intent, request.Binding.Endpoint, request.Binding.Model)
	if err != nil || !reflect.DeepEqual(expected, request.Binding) {
		return ErrRejected
	}
	_, cost, err := request.Binding.Model.ConservativeReservation()
	if err != nil {
		return ErrRejected
	}
	switch request.Intent.Reservation.BillingMode {
	case "api":
		if cost == nil || request.Intent.Reservation.CostMicroUSD == nil || *cost > *request.Intent.Reservation.CostMicroUSD {
			return ErrRejected
		}
	case "subscription":
		if request.Intent.Reservation.CostMicroUSD != nil {
			return ErrRejected
		}
	default:
		return ErrRejected
	}
	return nil
}

func (c *Client) postOnce(ctx context.Context, request Request, call providergateway.CallIntent) ([]byte, *responseEvidence, error) {
	var evidence *responseEvidence
	var raw []byte
	var observation *observedAttemptError
	retainObservation := func(observed *observedAttemptError) error {
		observation = observed
		return ErrPending
	}
	framing, err := providergateway.EffectiveResponseFraming(request.Expectation.ResponseFraming)
	if err != nil {
		return nil, nil, ErrPending
	}
	err = request.Lease.WithSecret(ctx, func(secretCtx context.Context, secret []byte) error {
		transport := newHTTPTransport(c.rootCAs, secretCtx)
		defer transport.CloseIdleConnections()
		client := &http.Client{
			Transport: transport,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return errors.New("provider redirect rejected")
			},
		}
		req, err := http.NewRequestWithContext(secretCtx, http.MethodPost, request.Binding.Endpoint.URL, &oneShotReader{data: request.Body})
		if err != nil {
			return ErrPending
		}
		req.ContentLength = int64(len(request.Body))
		req.GetBody = nil
		req.Header.Set("content-type", "application/json")
		expectedMediaType := "text/event-stream"
		if framing == providergateway.ResponseFramingJSON {
			expectedMediaType = "application/json"
		}
		req.Header.Set("accept", expectedMediaType)
		for name, value := range request.Binding.Endpoint.PublicHeaders {
			req.Header.Set(name, value)
		}
		if req.Header.Get("user-agent") == "" {
			req.Header.Set("user-agent", defaultUserAgent)
		}
		if request.Binding.Endpoint.SessionHeader != "" {
			sessionID, hashErr := canonical.Hash("harness.provider-session.v1", struct {
				InvocationID string `json:"invocation_id"`
			}{InvocationID: request.Binding.AccessInvocationID})
			if hashErr != nil {
				return ErrPending
			}
			req.Header.Set(request.Binding.Endpoint.SessionHeader, sessionID)
		}
		switch request.Binding.Endpoint.Auth.Scheme {
		case "bearer":
			req.Header.Set("authorization", "Bearer "+string(secret))
			defer req.Header.Del("authorization")
		case "api-key-header":
			name := request.Binding.Endpoint.Auth.HeaderName
			req.Header.Set(name, string(secret))
			defer req.Header.Del(name)
		default:
			return ErrPending
		}
		response, err := client.Do(req)
		if err != nil {
			return retainObservation(observedTransportFailure(err))
		}
		defer response.Body.Close()
		// Capture the bounded bytes before content-type checks or adapter interpretation.
		limit := request.Binding.Model.MaxResponseBytes
		body, readErr := io.ReadAll(io.LimitReader(response.Body, limit+1))
		complete := readErr == nil && int64(len(body)) <= limit
		evidence, err = captureResponseEvidence(request, call, response, body, complete)
		if err != nil {
			return ErrPending
		}
		fail := func(f *observedAttemptError, code, stage string, cause error) error {
			if err := attachDecodeEvidence(f, evidence, code, stage, cause); err != nil {
				return ErrPending
			}
			return retainObservation(f)
		}
		if readErr != nil {
			f := observedTransportFailure(readErr)
			if response.StatusCode != http.StatusOK {
				f = observedHTTPFailure(request.Binding.Model.AdapterID, response.StatusCode, nil)
			}
			return fail(f, "TRUNCATED_STREAM", "body-read", readErr)
		}
		if int64(len(body)) > limit {
			f := observedDecodeFailure(nil)
			if response.StatusCode != http.StatusOK {
				f = observedHTTPFailure(request.Binding.Model.AdapterID, response.StatusCode, nil)
			}
			return fail(f, "BODY_LIMIT_EXCEEDED", "body-read", errors.New("response exceeded admitted byte limit; captured prefix only, full body hash UNKNOWN"))
		}
		if response.StatusCode != http.StatusOK {
			return fail(observedHTTPFailure(request.Binding.Model.AdapterID, response.StatusCode, body), "HTTP_STATUS_REJECTED", "http-status", errors.New("provider HTTP status is not 200"))
		}
		mediaType, _, mediaErr := mime.ParseMediaType(response.Header.Get("content-type"))
		if mediaErr != nil || mediaType != expectedMediaType {
			if mediaErr == nil {
				mediaErr = errors.New("response content type differs from requested framing")
			}
			return fail(observedDecodeFailure(body), "UNEXPECTED_CONTENT_TYPE", "headers", mediaErr)
		}
		if len(body) == 0 {
			return fail(observedDecodeFailure(body), "TRUNCATED_STREAM", "body-read", errors.New("empty response body"))
		}
		raw = body
		return nil
	})
	if err != nil {
		if observation != nil {
			return nil, evidence, observation
		}
		return nil, evidence, err
	}
	return raw, evidence, nil
}

func newHTTPTransport(roots *x509.CertPool, ctx context.Context) *http.Transport {
	remaining := time.Until(deadline(ctx))
	short := min(remaining, 10*time.Second)
	return &http.Transport{
		Proxy:                  nil,
		DialContext:            (&net.Dialer{Timeout: short, KeepAlive: -1}).DialContext,
		ForceAttemptHTTP2:      false,
		DisableKeepAlives:      true,
		DisableCompression:     true,
		TLSClientConfig:        &tls.Config{RootCAs: roots.Clone(), MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:    short,
		ResponseHeaderTimeout:  remaining,
		ExpectContinueTimeout:  0,
		MaxResponseHeaderBytes: maximumHeaderBytes,
		MaxConnsPerHost:        1,
		MaxIdleConns:           0,
		MaxIdleConnsPerHost:    0,
		IdleConnTimeout:        0,
		WriteBufferSize:        0,
		ReadBufferSize:         0,
	}
}

func deadline(ctx context.Context) time.Time {
	value, _ := ctx.Deadline()
	return value
}

type oneShotReader struct {
	data   []byte
	offset int
}

// Read consumes the admitted request body without exposing a replay mechanism.
func (r *oneShotReader) Read(destination []byte) (int, error) {
	if r.offset >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(destination, r.data[r.offset:])
	r.offset += n
	return n, nil
}

func cloneUsage(source providergateway.Usage) providergateway.Usage {
	result := source
	result.ReasoningTokens = cloneInt64(source.ReasoningTokens)
	result.CacheReadTokens = cloneInt64(source.CacheReadTokens)
	result.CacheWriteTokens = cloneInt64(source.CacheWriteTokens)
	return result
}

func cloneSemantic(source *providergateway.ResponseSemanticProjection) *providergateway.ResponseSemanticProjection {
	if source == nil {
		return nil
	}
	result := *source
	if source.ToolCalls != nil {
		result.ToolCalls = make([]providergateway.ResponseToolIdentity, len(source.ToolCalls))
		copy(result.ToolCalls, source.ToolCalls)
	}
	if source.TerminalTool != nil {
		terminal := *source.TerminalTool
		result.TerminalTool = &terminal
	}
	return &result
}

func cloneInt64(source *int64) *int64 {
	if source == nil {
		return nil
	}
	value := *source
	return &value
}

func rejected(ctx context.Context) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return ErrRejected
}

func pending(ctx context.Context) error {
	if ctx != nil && ctx.Err() != nil {
		return errors.Join(ErrPending, ctx.Err())
	}
	return ErrPending
}
