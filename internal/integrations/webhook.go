package integrations

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
)

var (
	// ErrWebhookNotConfigured indicates inbound webhooks are disabled because no signing secret is present in env.
	ErrWebhookNotConfigured = errors.New("inbound webhook secret environment variable is unset; failing closed")
	// ErrWebhookUnauthorized indicates a missing, malformed, or invalid webhook signature/secret.
	ErrWebhookUnauthorized = errors.New("invalid or missing webhook signature")
)

// InboundTrigger is the verified, normalized event extracted from Jira, Linear, GitHub, GitLab, or generic webhooks.
type InboundTrigger struct {
	Provider     string   `json:"provider"`
	Event        string   `json:"event"`
	TicketKey    string   `json:"ticket_key,omitempty"`
	Title        string   `json:"title,omitempty"`
	Description  string   `json:"description,omitempty"`
	Labels       []string `json:"labels,omitempty"`
	URL          string   `json:"url,omitempty"`
	Branch       string   `json:"branch,omitempty"`
	Ignored      bool     `json:"ignored,omitempty"`
	IgnoreReason string   `json:"ignore_reason,omitempty"`
}

// Objective derives the agentic objective string from a verified inbound trigger.
func (tr InboundTrigger) Objective() string {
	if tr.TicketKey != "" || tr.Title != "" {
		return BuildObjective(Ticket{
			Provider:    tr.Provider,
			Key:         tr.TicketKey,
			Title:       firstNonEmpty(tr.Title, tr.Event),
			Description: tr.Description,
			URL:         tr.URL,
			Labels:      tr.Labels,
		})
	}
	return firstNonEmpty(strings.TrimSpace(tr.Description), fmt.Sprintf("Process %s webhook event %s", tr.Provider, tr.Event))
}

// VerifyHMACSHA256 verifies a hex-encoded HMAC-SHA256 signature (optionally prefixed with "sha256=")
// in constant time.
func VerifyHMACSHA256(secret string, body []byte, sigHeader string) bool {
	if secret == "" {
		return false
	}
	sig := strings.TrimSpace(sigHeader)
	sig = strings.TrimPrefix(sig, "sha256=")
	gotBytes, err := hex.DecodeString(sig)
	if err != nil || len(gotBytes) != sha256.Size {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	want := mac.Sum(nil)
	return subtle.ConstantTimeCompare(gotBytes, want) == 1
}

// VerifySharedSecret compares an inbound token header against secret in constant time.
func VerifySharedSecret(secret, got string) bool {
	if secret == "" || got == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(secret), []byte(strings.TrimSpace(got))) == 1
}

// ParseInboundWebhook authenticates headers + body fail-closed against cfg.SecretEnv
// and extracts a normalized InboundTrigger (marking Ignored if trigger_label does not match).
func ParseInboundWebhook(provider string, headers http.Header, body []byte, cfg *WebhookConfig) (InboundTrigger, error) {
	if cfg == nil {
		return InboundTrigger{}, ErrWebhookNotConfigured
	}
	secEnv := firstNonEmpty(cfg.SecretEnv, "FABRIC_WEBHOOK_SECRET")
	secret := strings.TrimSpace(os.Getenv(secEnv))
	if secret == "" {
		return InboundTrigger{}, ErrWebhookNotConfigured
	}

	prov := strings.ToLower(strings.TrimSpace(provider))
	if !authorizeWebhookRequest(prov, secret, headers, body) {
		return InboundTrigger{}, ErrWebhookUnauthorized
	}

	tr := InboundTrigger{Provider: prov}
	switch prov {
	case "jira":
		var payload struct {
			WebhookEvent string `json:"webhookEvent"`
			Issue        *struct {
				Key    string `json:"key"`
				Self   string `json:"self"`
				Fields struct {
					Summary     string   `json:"summary"`
					Description any      `json:"description"`
					Labels      []string `json:"labels"`
				} `json:"fields"`
			} `json:"issue"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			return InboundTrigger{}, fmt.Errorf("invalid jira webhook JSON: %w", err)
		}
		tr.Event = firstNonEmpty(payload.WebhookEvent, "jira.event")
		if payload.Issue != nil {
			tr.TicketKey = payload.Issue.Key
			tr.Title = payload.Issue.Fields.Summary
			tr.Description = extractJiraText(payload.Issue.Fields.Description)
			tr.Labels = payload.Issue.Fields.Labels
			tr.URL = payload.Issue.Self
		}
	case "linear":
		var payload struct {
			Action string `json:"action"`
			Type   string `json:"type"`
			URL    string `json:"url"`
			Data   struct {
				Identifier  string `json:"identifier"`
				ID          string `json:"id"`
				Title       string `json:"title"`
				Description string `json:"description"`
				URL         string `json:"url"`
				Labels      []struct {
					Name string `json:"name"`
				} `json:"labels"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			return InboundTrigger{}, fmt.Errorf("invalid linear webhook JSON: %w", err)
		}
		tr.Event = firstNonEmpty(strings.TrimSpace(payload.Type+"."+payload.Action), "linear.event")
		tr.TicketKey = firstNonEmpty(payload.Data.Identifier, payload.Data.ID)
		tr.Title = payload.Data.Title
		tr.Description = payload.Data.Description
		tr.URL = firstNonEmpty(payload.Data.URL, payload.URL)
		for _, l := range payload.Data.Labels {
			if l.Name != "" {
				tr.Labels = append(tr.Labels, l.Name)
			}
		}
	case "github":
		var payload struct {
			Action string `json:"action"`
			Issue  *struct {
				Number  int    `json:"number"`
				Title   string `json:"title"`
				Body    string `json:"body"`
				HTMLURL string `json:"html_url"`
				Labels  []struct {
					Name string `json:"name"`
				} `json:"labels"`
			} `json:"issue"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			return InboundTrigger{}, fmt.Errorf("invalid github webhook JSON: %w", err)
		}
		tr.Event = firstNonEmpty(headers.Get("X-GitHub-Event")+"."+payload.Action, payload.Action, "github.event")
		if payload.Issue != nil {
			tr.TicketKey = fmt.Sprintf("%d", payload.Issue.Number)
			tr.Title = payload.Issue.Title
			tr.Description = payload.Issue.Body
			tr.URL = payload.Issue.HTMLURL
			for _, l := range payload.Issue.Labels {
				if l.Name != "" {
					tr.Labels = append(tr.Labels, l.Name)
				}
			}
		}
	default:
		var payload struct {
			Event       string   `json:"event"`
			TicketKey   string   `json:"ticket_key"`
			Title       string   `json:"title"`
			Objective   string   `json:"objective"`
			Description string   `json:"description"`
			URL         string   `json:"url"`
			Branch      string   `json:"branch"`
			Labels      []string `json:"labels"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			return InboundTrigger{}, fmt.Errorf("invalid webhook JSON: %w", err)
		}
		tr.Event = firstNonEmpty(payload.Event, "webhook.trigger")
		tr.TicketKey = payload.TicketKey
		tr.Title = firstNonEmpty(payload.Title, payload.Objective)
		tr.Description = firstNonEmpty(payload.Description, payload.Objective)
		tr.Labels = payload.Labels
		tr.URL = payload.URL
		tr.Branch = payload.Branch
	}

	if wantLabel := strings.TrimSpace(cfg.TriggerLabel); wantLabel != "" && !hasLabel(tr.Labels, wantLabel) {
		tr.Ignored = true
		tr.IgnoreReason = fmt.Sprintf("missing required trigger_label %q", wantLabel)
	}
	return tr, nil
}

func authorizeWebhookRequest(provider, secret string, headers http.Header, body []byte) bool {
	if headers == nil {
		return false
	}
	switch provider {
	case "github":
		return VerifyHMACSHA256(secret, body, headers.Get("X-Hub-Signature-256"))
	case "linear":
		return VerifyHMACSHA256(secret, body, headers.Get("Linear-Signature"))
	default:
		if sig := firstNonEmpty(headers.Get("X-Fabric-Signature-256"), headers.Get("X-Hub-Signature-256")); sig != "" {
			return VerifyHMACSHA256(secret, body, sig)
		}
		if tok := firstNonEmpty(headers.Get("X-Webhook-Secret"), headers.Get("X-Gitlab-Token")); tok != "" {
			return VerifySharedSecret(secret, tok)
		}
		if auth := headers.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
			return VerifySharedSecret(secret, strings.TrimPrefix(auth, "Bearer "))
		}
		return false
	}
}

func hasLabel(labels []string, want string) bool {
	for _, l := range labels {
		if strings.EqualFold(strings.TrimSpace(l), want) {
			return true
		}
	}
	return false
}
