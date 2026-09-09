package draftpr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/go-github/v89/github"
	"harness.local/engorch/internal/canonical"
)

const githubAPIVersion = "2026-03-10"

// Client accesses the fixed public GitHub API through go-github. Credentials
// remain in memory and are excluded from plans, receipts and returned errors.
type Client struct {
	token string
	http  *http.Client
	api   *github.Client
}

type githubPolicyTransport struct{ owner *Client }

// RoundTrip applies fixed endpoint headers and validates one complete bounded
// JSON response before go-github decodes its endpoint model.
func (t githubPolicyTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if t.owner == nil || t.owner.http == nil || t.owner.http.Transport == nil || request == nil || request.URL == nil || request.URL.Scheme != "https" || request.URL.Host != "api.github.com" || request.URL.User != nil {
		return nil, errors.New("invalid GitHub request")
	}
	requestContext := request.Context()
	if t.owner.http.Timeout > 0 {
		var cancel context.CancelFunc
		requestContext, cancel = context.WithTimeout(requestContext, t.owner.http.Timeout)
		defer cancel()
	}
	req := request.Clone(requestContext)
	req.Header.Set("Authorization", "Bearer "+t.owner.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", githubAPIVersion)
	req.Header.Set("User-Agent", "engorch/1.0.0")
	response, err := t.owner.http.Transport.RoundTrip(req)
	if err != nil {
		return nil, errors.New("GitHub transport failed")
	}
	if response == nil || response.Body == nil {
		return nil, errors.New("GitHub response missing")
	}
	if response.ContentLength > canonical.MaxBytes {
		_ = response.Body.Close()
		return nil, errors.New("GitHub response exceeds bound")
	}
	kind, _, mediaErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if mediaErr != nil || kind != "application/json" {
		_ = response.Body.Close()
		return nil, errors.New("GitHub response is not JSON")
	}
	raw, readErr := io.ReadAll(io.LimitReader(response.Body, canonical.MaxBytes+1))
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil || len(raw) > canonical.MaxBytes {
		return nil, errors.New("GitHub response incomplete or oversized")
	}
	if response.ContentLength >= 0 && response.ContentLength != int64(len(raw)) {
		return nil, errors.New("GitHub response length mismatch")
	}
	if _, err := canonical.Normalize(raw); err != nil {
		return nil, errors.New("GitHub response is invalid JSON")
	}
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		if err := validateGitHubResponseShape(req, raw); err != nil {
			return nil, errors.New("GitHub response shape mismatch")
		}
	}
	response.Body = io.NopCloser(bytes.NewReader(raw))
	response.ContentLength = int64(len(raw))
	response.Request = req
	return response, nil
}

func validateGitHubResponseShape(request *http.Request, raw []byte) error {
	path := request.URL.Path
	switch {
	case request.Method == http.MethodGet && strings.Contains(path, "/git/ref/"):
		var ref string
		var object json.RawMessage
		if err := projectObject(raw, map[string]any{"ref": &ref, "object": &object}); err != nil {
			return err
		}
		var kind, sha string
		return projectObject(object, map[string]any{"type": &kind, "sha": &sha})
	case request.Method == http.MethodGet && strings.HasSuffix(path, "/pulls"):
		_, err := decodePage(raw)
		return err
	case request.Method == http.MethodGet && strings.Contains(path, "/pulls/") || request.Method == http.MethodPost && strings.HasSuffix(path, "/pulls"):
		_, err := DecodeObservation(raw)
		return err
	default:
		return errors.New("unsupported GitHub response endpoint")
	}
}

// NewClient creates an authenticated go-github client with bounded requests,
// no redirects, no ambient proxy and no application retries.
func NewClient(token string) (*Client, error) {
	if len(token) == 0 || len(token) > 4096 {
		return nil, errors.New("invalid GitHub credential")
	}
	for _, c := range token {
		if c < 33 || c > 126 {
			return nil, errors.New("invalid GitHub credential")
		}
	}
	transport := &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	client := &Client{token: token, http: &http.Client{Transport: transport, Timeout: 30 * time.Second}}
	wire := &http.Client{Transport: githubPolicyTransport{owner: client}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	api, err := github.NewClient(github.WithHTTPClient(wire), github.WithUserAgent("engorch/1.0.0"), github.WithDisableRateLimitCheck())
	if err != nil {
		transport.CloseIdleConnections()
		return nil, errors.New("cannot initialize GitHub client")
	}
	client.api = api
	return client, nil
}

// Close releases idle connections; it does not erase credentials from memory.
func (c *Client) Close() {
	if c != nil && c.http != nil {
		c.http.CloseIdleConnections()
	}
}

func (c *Client) initialized() bool {
	return c != nil && c.http != nil && c.api != nil && c.token != ""
}

// read exercises go-github's request construction while retaining a raw body
// only for transport-boundary tests. Production operations use typed services.
func (c *Client) read(ctx context.Context, path string) ([]byte, error) {
	if !c.initialized() {
		return nil, errors.New("uninitialized GitHub client")
	}
	request, err := c.api.NewRequest(ctx, http.MethodGet, strings.TrimPrefix(path, "/"), nil)
	if err != nil {
		return nil, errors.New("invalid GitHub request")
	}
	response, err := c.api.BareDo(request)
	if err != nil {
		return nil, githubError(ctx, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, errors.New("GitHub returned unexpected HTTP status")
	}
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, errors.New("GitHub response incomplete or oversized")
	}
	return raw, nil
}

func repositoryParts(repository string) (string, string, error) {
	if !repositoryName(repository) {
		return "", "", errors.New("invalid GitHub repository")
	}
	parts := strings.Split(repository, "/")
	return parts[0], parts[1], nil
}

func githubError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx != nil && ctx.Err() != nil {
		return errors.Join(errors.New("GitHub request failed"), ctx.Err())
	}
	var primary *github.RateLimitError
	var secondary *github.AbuseRateLimitError
	if errors.As(err, &primary) {
		return errors.New("GitHub primary rate limit reached")
	}
	if errors.As(err, &secondary) {
		return errors.New("GitHub secondary rate limit reached")
	}
	return errors.New("GitHub request failed")
}

func observationFromPull(repository string, pull *github.PullRequest) (Observation, error) {
	if pull == nil || pull.Number == nil || pull.HTMLURL == nil || pull.State == nil || pull.Draft == nil || pull.Title == nil || pull.Body == nil || pull.Head == nil || pull.Base == nil || pull.Head.Ref == nil || pull.Head.SHA == nil || pull.Head.Repo == nil || pull.Head.Repo.FullName == nil || pull.Base.Ref == nil || pull.Base.SHA == nil || pull.Base.Repo == nil || pull.Base.Repo.FullName == nil {
		return Observation{}, errors.New("incomplete GitHub pull response")
	}
	o := Observation{Number: *pull.Number, URL: *pull.HTMLURL, State: *pull.State, Draft: *pull.Draft, Title: *pull.Title, Body: *pull.Body, Head: BranchObservation{Repository: *pull.Head.Repo.FullName, Ref: *pull.Head.Ref, Commit: *pull.Head.SHA}, Base: BranchObservation{Repository: *pull.Base.Repo.FullName, Ref: *pull.Base.Ref, Commit: *pull.Base.SHA}}
	if o.Number <= 0 || o.URL != "https://github.com/"+repository+"/pull/"+strconv.Itoa(o.Number) || o.Head.Repository != repository || o.Base.Repository != repository {
		return Observation{}, errors.New("GitHub PR response differs from requested repository")
	}
	return o, nil
}

// Read retrieves one numbered PR without mutating hosted state.
func (c *Client) Read(ctx context.Context, repository string, number int) (Observation, error) {
	if !c.initialized() || number <= 0 {
		return Observation{}, errors.New("invalid GitHub PR locator")
	}
	owner, name, err := repositoryParts(repository)
	if err != nil {
		return Observation{}, err
	}
	pull, response, err := c.api.PullRequests.Get(ctx, owner, name, number)
	if err != nil {
		return Observation{}, githubError(ctx, err)
	}
	if response == nil || response.Response == nil || response.StatusCode != http.StatusOK {
		return Observation{}, errors.New("GitHub returned unexpected HTTP status")
	}
	o, err := observationFromPull(repository, pull)
	if err != nil || o.Number != number {
		return Observation{}, errors.New("GitHub PR response differs from requested locator")
	}
	return o, nil
}
