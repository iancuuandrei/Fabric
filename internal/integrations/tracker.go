package integrations

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/iancuuandrei/fabric/internal/githubflow"
)

const (
	defaultLinearURL = "https://api.linear.app/graphql"
	defaultGitLabURL = "https://gitlab.com"
)

// Ticket is the normalized external engineering issue representation across
// Jira, Linear, GitLab, and GitHub.
type Ticket struct {
	Provider    string   `json:"provider"`
	Key         string   `json:"key"`
	Title       string   `json:"title"`
	Description string   `json:"description,omitempty"`
	URL         string   `json:"url,omitempty"`
	State       string   `json:"state,omitempty"`
	Labels      []string `json:"labels,omitempty"`
	TeamID      string   `json:"team_id,omitempty"`
}

// Tracker reads, comments on, and transitions external engineering tickets.
type Tracker interface {
	Provider() string
	FetchTicket(ctx context.Context, key string) (Ticket, error)
	PostComment(ctx context.Context, key, body string) error
	Transition(ctx context.Context, key, targetState string) error
}

// BuildObjective formats a concise engineering objective from a normalized Ticket.
func BuildObjective(t Ticket) string {
	var b strings.Builder
	prov := strings.ToUpper(t.Provider)
	if prov == "" {
		prov = "TICKET"
	}
	fmt.Fprintf(&b, "Resolve [%s] %s: %s", prov, strings.TrimSpace(t.Key), strings.TrimSpace(t.Title))
	if t.URL != "" {
		fmt.Fprintf(&b, "\nURL: %s", t.URL)
	}
	if t.State != "" {
		fmt.Fprintf(&b, "\nState: %s", t.State)
	}
	if len(t.Labels) > 0 {
		fmt.Fprintf(&b, "\nLabels: %s", strings.Join(t.Labels, ", "))
	}
	if desc := strings.TrimSpace(t.Description); desc != "" {
		if len(desc) > 4096 {
			desc = desc[:4096] + "\n... [truncated]"
		}
		fmt.Fprintf(&b, "\n\nTicket Description:\n%s", desc)
	}
	return b.String()
}

// BranchSlug constructs a clean Git branch name derived from t.Provider and t.Key.
func BranchSlug(t Ticket) string {
	sanitize := func(s string) string {
		var out strings.Builder
		for _, r := range strings.ToLower(strings.TrimSpace(s)) {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
				out.WriteRune(r)
			}
		}
		return out.String()
	}
	p := sanitize(t.Provider)
	if p == "" {
		p = "ticket"
	}
	k := sanitize(t.Key)
	if k == "" {
		k = "issue"
	}
	slug := fmt.Sprintf("fabric/%s-%s", p, k)
	if len(slug) > 96 {
		slug = slug[:96]
	}
	return slug
}

// ResolveTracker selects and initializes the configured Tracker matching providerOverride
// (or the first configured tracker in cfg if providerOverride is empty).
func ResolveTracker(cfg *Config, providerOverride, projectOverride, repoRoot string, client *http.Client) (Tracker, error) {
	prov := strings.ToLower(strings.TrimSpace(providerOverride))
	if prov == "" && cfg != nil {
		switch {
		case cfg.Jira != nil:
			prov = "jira"
		case cfg.Linear != nil:
			prov = "linear"
		case cfg.GitLab != nil:
			prov = "gitlab"
		default:
			prov = "github"
		}
	}
	if prov == "" {
		prov = "github"
	}

	switch prov {
	case "jira":
		if cfg == nil || cfg.Jira == nil {
			return nil, errors.New("[integrations.jira] is not configured in fabric.toml")
		}
		return NewJiraTracker(*cfg.Jira, client)
	case "linear":
		var lc LinearConfig
		if cfg != nil && cfg.Linear != nil {
			lc = *cfg.Linear
		}
		return NewLinearTracker(lc, client)
	case "gitlab":
		var gl GitLabConfig
		if cfg != nil && cfg.GitLab != nil {
			gl = *cfg.GitLab
		}
		if strings.TrimSpace(projectOverride) != "" {
			gl.Project = strings.TrimSpace(projectOverride)
		}
		return NewGitLabTracker(gl, client)
	case "github":
		return NewGitHubTracker(repoRoot), nil
	default:
		return nil, fmt.Errorf("unsupported ticket provider %q (use jira, linear, gitlab, or github)", prov)
	}
}

// ResolveTrackerForRef infers provider and project from rawRef when omitted and
// returns (tracker, normalizedKey, error).
func ResolveTrackerForRef(cfg *Config, rawRef, providerOverride, projectOverride, repoRoot string, client *http.Client) (Tracker, string, error) {
	urlProv, parsedKey, urlProj := ParseReference(rawRef)
	key, err := ValidateTicketRef(parsedKey)
	if err != nil {
		return nil, "", err
	}
	prov := firstNonEmpty(strings.ToLower(strings.TrimSpace(providerOverride)), urlProv)
	if prov == "" {
		switch {
		case digitsOnlyRe.MatchString(key):
			if cfg != nil && cfg.GitLab != nil && cfg.GitHub == nil {
				prov = "gitlab"
			} else {
				prov = "github"
			}
		case issueKeyRe.MatchString(key):
			if cfg != nil && cfg.Jira != nil {
				prov = "jira"
			} else if cfg != nil && cfg.Linear != nil {
				prov = "linear"
			} else {
				prov = "jira"
			}
		}
	}
	proj := firstNonEmpty(strings.TrimSpace(projectOverride), urlProj)
	tr, err := ResolveTracker(cfg, prov, proj, repoRoot, client)
	if err != nil {
		return nil, "", err
	}
	return tr, key, nil
}

// JiraTracker integrates with Jira Cloud (v3 ADF) and Jira Server/DC (v2).
type JiraTracker struct {
	cfg    JiraConfig
	client *http.Client
}

// NewJiraTracker creates a validated JiraTracker.
func NewJiraTracker(cfg JiraConfig, client *http.Client) (*JiraTracker, error) {
	if err := ValidateEndpointURL(cfg.BaseURL); err != nil {
		return nil, err
	}
	ver := firstNonEmpty(cfg.APIVersion, "3")
	if ver != "2" && ver != "3" {
		return nil, fmt.Errorf("unsupported Jira API version %q", cfg.APIVersion)
	}
	cfg.APIVersion = ver
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	cfg.TokenEnv = firstNonEmpty(cfg.TokenEnv, "JIRA_API_TOKEN")
	return &JiraTracker{cfg: cfg, client: client}, nil
}

// Provider reports "jira".
func (j *JiraTracker) Provider() string { return "jira" }

func (j *JiraTracker) authHeaders() (map[string]string, error) {
	token, err := requireEnv(j.cfg.TokenEnv, "Jira")
	if err != nil {
		return nil, err
	}
	email := ""
	if j.cfg.EmailEnv != "" {
		email = strings.TrimSpace(os.Getenv(j.cfg.EmailEnv))
	}
	if email != "" {
		creds := base64.StdEncoding.EncodeToString([]byte(email + ":" + token))
		return map[string]string{"Authorization": "Basic " + creds}, nil
	}
	return map[string]string{"Authorization": "Bearer " + token}, nil
}

// FetchTicket retrieves a Jira issue via REST API v3 (ADF) or v2.
func (j *JiraTracker) FetchTicket(ctx context.Context, key string) (Ticket, error) {
	cleanKey, err := ValidateTicketRef(key)
	if err != nil {
		return Ticket{}, err
	}
	hdrs, err := j.authHeaders()
	if err != nil {
		return Ticket{}, err
	}
	endpoint := fmt.Sprintf("%s/rest/api/%s/issue/%s?fields=summary,description,status,labels",
		j.cfg.BaseURL, j.cfg.APIVersion, url.PathEscape(cleanKey))
	var resp struct {
		Key    string `json:"key"`
		Fields struct {
			Summary     string   `json:"summary"`
			Description any      `json:"description"`
			Labels      []string `json:"labels"`
			Status      struct {
				Name string `json:"name"`
			} `json:"status"`
		} `json:"fields"`
	}
	code, raw, err := doJSON(ctx, j.client, http.MethodGet, endpoint, hdrs, nil, &resp)
	if err != nil {
		return Ticket{}, err
	}
	if code < 200 || code >= 300 {
		return Ticket{}, httpError("Jira FetchTicket", code, raw)
	}
	k := firstNonEmpty(resp.Key, cleanKey)
	return Ticket{
		Provider:    "jira",
		Key:         k,
		Title:       resp.Fields.Summary,
		Description: extractJiraText(resp.Fields.Description),
		URL:         j.cfg.BaseURL + "/browse/" + url.PathEscape(k),
		State:       resp.Fields.Status.Name,
		Labels:      resp.Fields.Labels,
	}, nil
}

// PostComment posts a Jira comment wrapped in ADF (API v3) or plain text (API v2).
func (j *JiraTracker) PostComment(ctx context.Context, key, body string) error {
	cleanKey, err := ValidateTicketRef(key)
	if err != nil {
		return err
	}
	if strings.TrimSpace(body) == "" {
		return errors.New("comment body cannot be empty")
	}
	hdrs, err := j.authHeaders()
	if err != nil {
		return err
	}
	endpoint := fmt.Sprintf("%s/rest/api/%s/issue/%s/comment",
		j.cfg.BaseURL, j.cfg.APIVersion, url.PathEscape(cleanKey))
	var payload any
	if j.cfg.APIVersion == "3" {
		payload = map[string]any{
			"body": map[string]any{
				"type":    "doc",
				"version": 1,
				"content": []map[string]any{
					{
						"type": "paragraph",
						"content": []map[string]any{
							{"type": "text", "text": body},
						},
					},
				},
			},
		}
	} else {
		payload = map[string]any{"body": body}
	}
	code, raw, err := doJSON(ctx, j.client, http.MethodPost, endpoint, hdrs, payload, nil)
	if err != nil {
		return err
	}
	if code < 200 || code >= 300 {
		return httpError("Jira PostComment", code, raw)
	}
	return nil
}

// Transition moves a Jira issue to targetState (matching transition name or destination state).
func (j *JiraTracker) Transition(ctx context.Context, key, targetState string) error {
	cleanKey, err := ValidateTicketRef(key)
	if err != nil {
		return err
	}
	want := strings.TrimSpace(targetState)
	if want == "" {
		return errors.New("target state cannot be empty")
	}
	hdrs, err := j.authHeaders()
	if err != nil {
		return err
	}
	endpoint := fmt.Sprintf("%s/rest/api/%s/issue/%s/transitions",
		j.cfg.BaseURL, j.cfg.APIVersion, url.PathEscape(cleanKey))
	var trResp struct {
		Transitions []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			To   struct {
				Name string `json:"name"`
			} `json:"to"`
		} `json:"transitions"`
	}
	code, raw, err := doJSON(ctx, j.client, http.MethodGet, endpoint, hdrs, nil, &trResp)
	if err != nil {
		return err
	}
	if code < 200 || code >= 300 {
		return httpError("Jira ListTransitions", code, raw)
	}
	var matchID string
	avail := make([]string, 0, len(trResp.Transitions))
	for _, tr := range trResp.Transitions {
		avail = append(avail, tr.Name)
		if strings.EqualFold(tr.Name, want) || strings.EqualFold(tr.To.Name, want) {
			matchID = tr.ID
			break
		}
	}
	if matchID == "" {
		return fmt.Errorf("Jira transition %q not available on %s (available: %s)", want, cleanKey, strings.Join(avail, ", "))
	}
	code, raw, err = doJSON(ctx, j.client, http.MethodPost, endpoint, hdrs, map[string]any{
		"transition": map[string]string{"id": matchID},
	}, nil)
	if err != nil {
		return err
	}
	if code < 200 || code >= 300 {
		return httpError("Jira Transition", code, raw)
	}
	return nil
}

func extractJiraText(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case map[string]any:
		return extractADFText(x)
	default:
		return fmt.Sprintf("%v", x)
	}
}

func extractADFText(node map[string]any) string {
	if node == nil {
		return ""
	}
	if txt, ok := node["text"].(string); ok {
		return txt
	}
	children, ok := node["content"].([]any)
	if !ok || len(children) == 0 {
		return ""
	}
	parts := make([]string, 0, len(children))
	for _, ch := range children {
		if m, ok := ch.(map[string]any); ok {
			if s := strings.TrimSpace(extractADFText(m)); s != "" {
				parts = append(parts, s)
			}
		}
	}
	nodeType, _ := node["type"].(string)
	if nodeType == "doc" {
		return strings.Join(parts, "\n\n")
	}
	return strings.Join(parts, "")
}

// LinearTracker integrates with Linear's GraphQL API.
type LinearTracker struct {
	cfg    LinearConfig
	client *http.Client
}

// NewLinearTracker creates a validated LinearTracker.
func NewLinearTracker(cfg LinearConfig, client *http.Client) (*LinearTracker, error) {
	cfg.BaseURL = strings.TrimRight(firstNonEmpty(cfg.BaseURL, defaultLinearURL), "/")
	if err := ValidateEndpointURL(cfg.BaseURL); err != nil {
		return nil, err
	}
	cfg.TokenEnv = firstNonEmpty(cfg.TokenEnv, "LINEAR_API_KEY")
	return &LinearTracker{cfg: cfg, client: client}, nil
}

// Provider reports "linear".
func (l *LinearTracker) Provider() string { return "linear" }

func (l *LinearTracker) query(ctx context.Context, gql string, vars map[string]any, dst any) error {
	tok, err := requireEnv(l.cfg.TokenEnv, "Linear")
	if err != nil {
		return err
	}
	hdrs := map[string]string{"Authorization": tok}
	var env struct {
		Data   any `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	env.Data = dst
	code, raw, err := doJSON(ctx, l.client, http.MethodPost, l.cfg.BaseURL, hdrs, map[string]any{
		"query":     gql,
		"variables": vars,
	}, &env)
	if err != nil {
		return err
	}
	if code < 200 || code >= 300 {
		return httpError("Linear GraphQL", code, raw)
	}
	return joinGraphQLErrors(env.Errors)
}

func (l *LinearTracker) fetchIssueIDs(ctx context.Context, key string) (Ticket, string, error) {
	cleanKey, err := ValidateTicketRef(key)
	if err != nil {
		return Ticket{}, "", err
	}
	const q = `query($id: String!) {
  issue(id: $id) {
    id
    identifier
    title
    description
    url
    state { name }
    labels { nodes { name } }
    team { id }
  }
}`
	var data struct {
		Issue *struct {
			ID          string `json:"id"`
			Identifier  string `json:"identifier"`
			Title       string `json:"title"`
			Description string `json:"description"`
			URL         string `json:"url"`
			State       *struct {
				Name string `json:"name"`
			} `json:"state"`
			Labels *struct {
				Nodes []struct {
					Name string `json:"name"`
				} `json:"nodes"`
			} `json:"labels"`
			Team *struct {
				ID string `json:"id"`
			} `json:"team"`
		} `json:"issue"`
	}
	if err := l.query(ctx, q, map[string]any{"id": cleanKey}, &data); err != nil {
		return Ticket{}, "", err
	}
	if data.Issue == nil {
		return Ticket{}, "", fmt.Errorf("Linear issue %q not found", cleanKey)
	}
	t := Ticket{
		Provider:    "linear",
		Key:         firstNonEmpty(data.Issue.Identifier, cleanKey),
		Title:       data.Issue.Title,
		Description: data.Issue.Description,
		URL:         data.Issue.URL,
	}
	if data.Issue.State != nil {
		t.State = data.Issue.State.Name
	}
	if data.Issue.Team != nil {
		t.TeamID = data.Issue.Team.ID
	}
	if data.Issue.Labels != nil {
		for _, n := range data.Issue.Labels.Nodes {
			if n.Name != "" {
				t.Labels = append(t.Labels, n.Name)
			}
		}
	}
	return t, data.Issue.ID, nil
}

// FetchTicket retrieves a Linear issue by identifier (e.g. ENG-42) or UUID.
func (l *LinearTracker) FetchTicket(ctx context.Context, key string) (Ticket, error) {
	t, _, err := l.fetchIssueIDs(ctx, key)
	return t, err
}

// PostComment creates a Markdown comment on a Linear issue.
func (l *LinearTracker) PostComment(ctx context.Context, key, body string) error {
	if strings.TrimSpace(body) == "" {
		return errors.New("comment body cannot be empty")
	}
	_, issueUUID, err := l.fetchIssueIDs(ctx, key)
	if err != nil {
		return err
	}
	const m = `mutation($id: String!, $body: String!) {
  commentCreate(input: { issueId: $id, body: $body }) { success }
}`
	var data struct {
		CommentCreate struct {
			Success bool `json:"success"`
		} `json:"commentCreate"`
	}
	if err := l.query(ctx, m, map[string]any{"id": issueUUID, "body": body}, &data); err != nil {
		return err
	}
	if !data.CommentCreate.Success {
		return errors.New("Linear commentCreate returned success=false")
	}
	return nil
}

// Transition moves a Linear issue to targetState using its team workflow states.
func (l *LinearTracker) Transition(ctx context.Context, key, targetState string) error {
	want := strings.TrimSpace(targetState)
	if want == "" {
		return errors.New("target state cannot be empty")
	}
	t, issueUUID, err := l.fetchIssueIDs(ctx, key)
	if err != nil {
		return err
	}
	if t.TeamID == "" {
		return fmt.Errorf("Linear issue %q did not expose team workflow states", key)
	}
	const qStates = `query($teamId: String!) {
  team(id: $teamId) {
    states { nodes { id name } }
  }
}`
	var sData struct {
		Team *struct {
			States struct {
				Nodes []struct {
					ID   string `json:"id"`
					Name string `json:"name"`
				} `json:"nodes"`
			} `json:"states"`
		} `json:"team"`
	}
	if err := l.query(ctx, qStates, map[string]any{"teamId": t.TeamID}, &sData); err != nil {
		return err
	}
	var stateID string
	var names []string
	if sData.Team != nil {
		for _, st := range sData.Team.States.Nodes {
			names = append(names, st.Name)
			if strings.EqualFold(st.Name, want) {
				stateID = st.ID
				break
			}
		}
	}
	if stateID == "" {
		return fmt.Errorf("Linear state %q not found on team (available: %s)", want, strings.Join(names, ", "))
	}
	const mUpdate = `mutation($id: String!, $stateId: String!) {
  issueUpdate(id: $id, input: { stateId: $stateId }) { success }
}`
	var uData struct {
		IssueUpdate struct {
			Success bool `json:"success"`
		} `json:"issueUpdate"`
	}
	if err := l.query(ctx, mUpdate, map[string]any{"id": issueUUID, "stateId": stateID}, &uData); err != nil {
		return err
	}
	if !uData.IssueUpdate.Success {
		return errors.New("Linear issueUpdate returned success=false")
	}
	return nil
}

// GitLabTracker integrates with GitLab's REST API v4.
type GitLabTracker struct {
	cfg    GitLabConfig
	client *http.Client
}

// NewGitLabTracker creates a validated GitLabTracker.
func NewGitLabTracker(cfg GitLabConfig, client *http.Client) (*GitLabTracker, error) {
	cfg.BaseURL = strings.TrimRight(firstNonEmpty(cfg.BaseURL, defaultGitLabURL), "/")
	if err := ValidateEndpointURL(cfg.BaseURL); err != nil {
		return nil, err
	}
	if strings.TrimSpace(cfg.Project) == "" {
		return nil, errors.New("GitLab project path (group/repo) is required in [integrations.gitlab] or --project")
	}
	cfg.TokenEnv = firstNonEmpty(cfg.TokenEnv, "GITLAB_TOKEN")
	return &GitLabTracker{cfg: cfg, client: client}, nil
}

// Provider reports "gitlab".
func (g *GitLabTracker) Provider() string { return "gitlab" }

func (g *GitLabTracker) issueEndpoint(key string) (string, error) {
	cleanKey, err := ValidateTicketRef(key)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s/api/v4/projects/%s/issues/%s",
		g.cfg.BaseURL, url.PathEscape(strings.Trim(g.cfg.Project, "/")), url.PathEscape(cleanKey)), nil
}

// FetchTicket retrieves a GitLab issue by IID.
func (g *GitLabTracker) FetchTicket(ctx context.Context, key string) (Ticket, error) {
	ep, err := g.issueEndpoint(key)
	if err != nil {
		return Ticket{}, err
	}
	tok, err := requireEnv(g.cfg.TokenEnv, "GitLab")
	if err != nil {
		return Ticket{}, err
	}
	var resp struct {
		IID         int      `json:"iid"`
		Title       string   `json:"title"`
		Description string   `json:"description"`
		WebURL      string   `json:"web_url"`
		State       string   `json:"state"`
		Labels      []string `json:"labels"`
	}
	code, raw, err := doJSON(ctx, g.client, http.MethodGet, ep, map[string]string{"PRIVATE-TOKEN": tok}, nil, &resp)
	if err != nil {
		return Ticket{}, err
	}
	if code < 200 || code >= 300 {
		return Ticket{}, httpError("GitLab FetchTicket", code, raw)
	}
	k := key
	if resp.IID > 0 {
		k = strconv.Itoa(resp.IID)
	}
	return Ticket{
		Provider:    "gitlab",
		Key:         k,
		Title:       resp.Title,
		Description: resp.Description,
		URL:         resp.WebURL,
		State:       resp.State,
		Labels:      resp.Labels,
	}, nil
}

// PostComment adds a note on a GitLab issue.
func (g *GitLabTracker) PostComment(ctx context.Context, key, body string) error {
	if strings.TrimSpace(body) == "" {
		return errors.New("comment body cannot be empty")
	}
	ep, err := g.issueEndpoint(key)
	if err != nil {
		return err
	}
	tok, err := requireEnv(g.cfg.TokenEnv, "GitLab")
	if err != nil {
		return err
	}
	code, raw, err := doJSON(ctx, g.client, http.MethodPost, ep+"/notes",
		map[string]string{"PRIVATE-TOKEN": tok}, map[string]string{"body": body}, nil)
	if err != nil {
		return err
	}
	if code < 200 || code >= 300 {
		return httpError("GitLab PostComment", code, raw)
	}
	return nil
}

// Transition closes, reopens, or adds a label on a GitLab issue.
func (g *GitLabTracker) Transition(ctx context.Context, key, targetState string) error {
	want := strings.TrimSpace(targetState)
	if want == "" {
		return errors.New("target state cannot be empty")
	}
	ep, err := g.issueEndpoint(key)
	if err != nil {
		return err
	}
	tok, err := requireEnv(g.cfg.TokenEnv, "GitLab")
	if err != nil {
		return err
	}
	payload := map[string]string{}
	switch strings.ToLower(want) {
	case "closed", "close", "done":
		payload["state_event"] = "close"
	case "open", "reopen":
		payload["state_event"] = "reopen"
	default:
		payload["add_labels"] = want
	}
	code, raw, err := doJSON(ctx, g.client, http.MethodPut, ep, map[string]string{"PRIVATE-TOKEN": tok}, payload, nil)
	if err != nil {
		return err
	}
	if code < 200 || code >= 300 {
		return httpError("GitLab Transition", code, raw)
	}
	return nil
}

// GitHubTracker adapts Fabric's `githubflow` `gh` CLI wrapper to the unified Tracker interface.
type GitHubTracker struct {
	repoRoot string
}

// NewGitHubTracker returns a Tracker backed by GitHub Issues (`gh`).
func NewGitHubTracker(repoRoot string) *GitHubTracker {
	return &GitHubTracker{repoRoot: repoRoot}
}

// Provider reports "github".
func (g *GitHubTracker) Provider() string { return "github" }

// FetchTicket retrieves a GitHub issue via `gh`.
func (g *GitHubTracker) FetchTicket(ctx context.Context, key string) (Ticket, error) {
	iss, err := githubflow.FetchIssue(ctx, g.repoRoot, key)
	if err != nil {
		return Ticket{}, err
	}
	k := key
	if iss.Number > 0 {
		k = strconv.Itoa(iss.Number)
	}
	return Ticket{
		Provider:    "github",
		Key:         k,
		Title:       iss.Title,
		Description: iss.Body,
		URL:         iss.URL,
		Labels:      iss.Labels,
	}, nil
}

// PostComment posts a comment on a GitHub issue via `gh`.
func (g *GitHubTracker) PostComment(ctx context.Context, key, body string) error {
	return githubflow.CommentOnIssue(ctx, g.repoRoot, key, body)
}

// Transition closes, reopens, or adds a label to a GitHub issue via `gh`.
func (g *GitHubTracker) Transition(ctx context.Context, key, targetState string) error {
	return githubflow.TransitionIssue(ctx, g.repoRoot, key, targetState)
}
