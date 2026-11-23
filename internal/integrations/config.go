package integrations

import (
	"errors"
	"fmt"
	"math"
	"net"
	"net/url"
	"os"
	"regexp"
	"strings"
)

var validEnvNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Config holds external engineering tracker, chat notification, and inbound webhook
// settings parsed from `[integrations]` in fabric.toml. Secret values are never stored
// in configuration; all credentials and chat webhook URLs reference env var names (`*_env`).
type Config struct {
	Jira     *JiraConfig    `toml:"jira" json:"jira,omitempty"`
	Linear   *LinearConfig  `toml:"linear" json:"linear,omitempty"`
	GitLab   *GitLabConfig  `toml:"gitlab" json:"gitlab,omitempty"`
	GitHub   *GitHubConfig  `toml:"github" json:"github,omitempty"`
	Notify   *NotifyConfig  `toml:"notify" json:"notify,omitempty"`
	Webhooks *WebhookConfig `toml:"webhooks" json:"webhooks,omitempty"`
}

// JiraConfig configures Jira Cloud (API v3 + Atlassian Document Format) or
// Jira Server / Data Center (API v2 + PAT or Basic auth).
type JiraConfig struct {
	BaseURL         string `toml:"base_url" json:"base_url"`
	EmailEnv        string `toml:"email_env" json:"email_env,omitempty"`
	TokenEnv        string `toml:"token_env" json:"token_env,omitempty"`
	APIVersion      string `toml:"api_version" json:"api_version,omitempty"` // "3" (default) or "2"
	AutoComment     bool   `toml:"auto_comment" json:"auto_comment,omitempty"`
	ReadyTransition string `toml:"ready_transition" json:"ready_transition,omitempty"`
}

// LinearConfig configures Linear GraphQL issue tracking and workflow state updates.
type LinearConfig struct {
	BaseURL     string `toml:"base_url" json:"base_url,omitempty"`
	TokenEnv    string `toml:"token_env" json:"token_env,omitempty"`
	AutoComment bool   `toml:"auto_comment" json:"auto_comment,omitempty"`
	ReadyState  string `toml:"ready_state" json:"ready_state,omitempty"`
}

// GitLabConfig configures GitLab REST v4 issue lookup, comments, and state transitions.
type GitLabConfig struct {
	BaseURL     string `toml:"base_url" json:"base_url,omitempty"`
	Project     string `toml:"project" json:"project"`
	TokenEnv    string `toml:"token_env" json:"token_env,omitempty"`
	AutoComment bool   `toml:"auto_comment" json:"auto_comment,omitempty"`
}

// GitHubConfig configures default GitHub issue automation via `gh`.
type GitHubConfig struct {
	AutoComment bool `toml:"auto_comment" json:"auto_comment,omitempty"`
}

// NotifyConfig configures team chat alerts and HMAC-signed outbound webhooks.
type NotifyConfig struct {
	SlackWebhookEnv   string   `toml:"slack_webhook_env" json:"slack_webhook_env,omitempty"`
	DiscordWebhookEnv string   `toml:"discord_webhook_env" json:"discord_webhook_env,omitempty"`
	TeamsWebhookEnv   string   `toml:"teams_webhook_env" json:"teams_webhook_env,omitempty"`
	WebhookURL        string   `toml:"webhook_url" json:"webhook_url,omitempty"`
	WebhookSecretEnv  string   `toml:"webhook_secret_env" json:"webhook_secret_env,omitempty"`
	Events            []string `toml:"events" json:"events,omitempty"`
}

// WebhookConfig governs verified inbound webhook triggers (`/v1/webhooks/{provider}`).
type WebhookConfig struct {
	SecretEnv       string  `toml:"secret_env" json:"secret_env"`
	TriggerLabel    string  `toml:"trigger_label" json:"trigger_label,omitempty"`
	DefaultMaxUSD   float64 `toml:"default_max_usd" json:"default_max_usd,omitempty"`
	DefaultMaxTurns int     `toml:"default_max_turns" json:"default_max_turns,omitempty"`
	AutoComment     bool    `toml:"auto_comment" json:"auto_comment,omitempty"`
	AutoNotify      bool    `toml:"auto_notify" json:"auto_notify,omitempty"`
}

// ValidateEndpointURL enforces SSRF-safe external HTTPS URLs or explicit loopback HTTP URLs,
// rejecting embedded credentials and invalid schemes.
func ValidateEndpointURL(raw string) error {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return errors.New("integration endpoint URL cannot be empty")
	}
	u, err := url.Parse(trimmed)
	if err != nil {
		return fmt.Errorf("invalid integration endpoint URL: %w", err)
	}
	if u.User != nil {
		return errors.New("integration endpoint URL must not embed credentials")
	}
	host := u.Hostname()
	if host == "" {
		return errors.New("integration endpoint URL must include a host")
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return nil
	case "http":
		if strings.EqualFold(host, "localhost") {
			return nil
		}
		if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
			return nil
		}
		return fmt.Errorf("non-loopback HTTP endpoint rejected (%s): use https://", host)
	default:
		return fmt.Errorf("unsupported URL scheme %q (use https://)", u.Scheme)
	}
}

// ValidateEnvVarName ensures `*_env` settings hold an environment variable name rather
// than a raw secret, URL, or header value.
func ValidateEnvVarName(name string) error {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return nil
	}
	if strings.Contains(trimmed, "://") || strings.ContainsAny(trimmed, ": \t\r\n=") || !validEnvNameRe.MatchString(trimmed) {
		return fmt.Errorf("invalid environment variable name %q (pass an env var name, never a raw token or URL)", name)
	}
	return nil
}

// Validate verifies URL safety, secrets hygiene, and bounded limits across all integration sections.
func (c *Config) Validate() error {
	if c == nil {
		return nil
	}
	if c.Jira != nil {
		if err := ValidateEndpointURL(c.Jira.BaseURL); err != nil {
			return fmt.Errorf("integrations.jira.base_url: %w", err)
		}
		if err := ValidateEnvVarName(c.Jira.EmailEnv); err != nil {
			return fmt.Errorf("integrations.jira.email_env: %w", err)
		}
		if err := ValidateEnvVarName(c.Jira.TokenEnv); err != nil {
			return fmt.Errorf("integrations.jira.token_env: %w", err)
		}
		if ver := strings.TrimSpace(c.Jira.APIVersion); ver != "" && ver != "2" && ver != "3" {
			return fmt.Errorf("integrations.jira.api_version must be \"2\" or \"3\", got %q", c.Jira.APIVersion)
		}
	}
	if c.Linear != nil {
		if strings.TrimSpace(c.Linear.BaseURL) != "" {
			if err := ValidateEndpointURL(c.Linear.BaseURL); err != nil {
				return fmt.Errorf("integrations.linear.base_url: %w", err)
			}
		}
		if err := ValidateEnvVarName(c.Linear.TokenEnv); err != nil {
			return fmt.Errorf("integrations.linear.token_env: %w", err)
		}
	}
	if c.GitLab != nil {
		if strings.TrimSpace(c.GitLab.BaseURL) != "" {
			if err := ValidateEndpointURL(c.GitLab.BaseURL); err != nil {
				return fmt.Errorf("integrations.gitlab.base_url: %w", err)
			}
		}
		if strings.TrimSpace(c.GitLab.Project) == "" {
			return errors.New("integrations.gitlab.project is required when [integrations.gitlab] is configured")
		}
		if err := ValidateEnvVarName(c.GitLab.TokenEnv); err != nil {
			return fmt.Errorf("integrations.gitlab.token_env: %w", err)
		}
	}
	if c.Notify != nil {
		for field, envName := range map[string]string{
			"slack_webhook_env":   c.Notify.SlackWebhookEnv,
			"discord_webhook_env": c.Notify.DiscordWebhookEnv,
			"teams_webhook_env":   c.Notify.TeamsWebhookEnv,
			"webhook_secret_env":  c.Notify.WebhookSecretEnv,
		} {
			if err := ValidateEnvVarName(envName); err != nil {
				return fmt.Errorf("integrations.notify.%s: %w", field, err)
			}
		}
		if strings.TrimSpace(c.Notify.WebhookURL) != "" {
			if err := ValidateEndpointURL(c.Notify.WebhookURL); err != nil {
				return fmt.Errorf("integrations.notify.webhook_url: %w", err)
			}
		}
	}
	if c.Webhooks != nil {
		if err := ValidateEnvVarName(c.Webhooks.SecretEnv); err != nil {
			return fmt.Errorf("integrations.webhooks.secret_env: %w", err)
		}
		if math.IsNaN(c.Webhooks.DefaultMaxUSD) || math.IsInf(c.Webhooks.DefaultMaxUSD, 0) || c.Webhooks.DefaultMaxUSD < 0 {
			return errors.New("integrations.webhooks.default_max_usd must be >= 0")
		}
		if c.Webhooks.DefaultMaxTurns < 0 || c.Webhooks.DefaultMaxTurns > 200 {
			return errors.New("integrations.webhooks.default_max_turns must be 0..200")
		}
	}
	return nil
}

// IntegrationStatus describes readiness for one tracker, notification channel, or webhook route.
type IntegrationStatus struct {
	Name       string   `json:"name"`
	Category   string   `json:"category"` // tracker | notify | webhook
	Configured bool     `json:"configured"`
	Ready      bool     `json:"ready"`
	Endpoint   string   `json:"endpoint,omitempty"`
	MissingEnv []string `json:"missing_env,omitempty"`
	Details    string   `json:"details,omitempty"`
}

// StatusReport summarizes configured integrations without ever returning secret values.
type StatusReport struct {
	Items []IntegrationStatus `json:"items"`
}

// InspectStatus evaluates configured integrations and checks whether referenced env vars are set.
func InspectStatus(cfg *Config) StatusReport {
	var rep StatusReport
	checkEnv := func(envName string, missing *[]string) bool {
		if envName == "" {
			return true
		}
		if strings.TrimSpace(os.Getenv(envName)) == "" {
			*missing = append(*missing, envName)
			return false
		}
		return true
	}

	if cfg != nil && cfg.Jira != nil {
		tokEnv := firstNonEmpty(cfg.Jira.TokenEnv, "JIRA_API_TOKEN")
		var missing []string
		checkEnv(tokEnv, &missing)
		if cfg.Jira.EmailEnv != "" {
			checkEnv(cfg.Jira.EmailEnv, &missing)
		}
		ver := firstNonEmpty(cfg.Jira.APIVersion, "3")
		rep.Items = append(rep.Items, IntegrationStatus{
			Name:       "jira",
			Category:   "tracker",
			Configured: true,
			Ready:      len(missing) == 0,
			Endpoint:   cfg.Jira.BaseURL,
			MissingEnv: missing,
			Details:    fmt.Sprintf("REST API v%s", ver),
		})
	} else {
		rep.Items = append(rep.Items, IntegrationStatus{Name: "jira", Category: "tracker"})
	}

	if cfg != nil && cfg.Linear != nil {
		tokEnv := firstNonEmpty(cfg.Linear.TokenEnv, "LINEAR_API_KEY")
		var missing []string
		checkEnv(tokEnv, &missing)
		rep.Items = append(rep.Items, IntegrationStatus{
			Name:       "linear",
			Category:   "tracker",
			Configured: true,
			Ready:      len(missing) == 0,
			Endpoint:   firstNonEmpty(cfg.Linear.BaseURL, defaultLinearURL),
			MissingEnv: missing,
			Details:    "GraphQL API",
		})
	} else {
		rep.Items = append(rep.Items, IntegrationStatus{Name: "linear", Category: "tracker"})
	}

	if cfg != nil && cfg.GitLab != nil {
		tokEnv := firstNonEmpty(cfg.GitLab.TokenEnv, "GITLAB_TOKEN")
		var missing []string
		checkEnv(tokEnv, &missing)
		rep.Items = append(rep.Items, IntegrationStatus{
			Name:       "gitlab",
			Category:   "tracker",
			Configured: true,
			Ready:      len(missing) == 0,
			Endpoint:   firstNonEmpty(cfg.GitLab.BaseURL, defaultGitLabURL),
			MissingEnv: missing,
			Details:    "project=" + cfg.GitLab.Project,
		})
	} else {
		rep.Items = append(rep.Items, IntegrationStatus{Name: "gitlab", Category: "tracker"})
	}

	ghConfigured := cfg != nil && cfg.GitHub != nil
	rep.Items = append(rep.Items, IntegrationStatus{
		Name:       "github",
		Category:   "tracker",
		Configured: ghConfigured,
		Ready:      true,
		Details:    "Uses authenticated `gh` CLI",
	})

	var notify *NotifyConfig
	if cfg != nil {
		notify = cfg.Notify
	}
	appendNotifyStatus(&rep, "slack", envOrDefault(notify, func(n *NotifyConfig) string { return n.SlackWebhookEnv }, "FABRIC_SLACK_WEBHOOK"), checkEnv)
	appendNotifyStatus(&rep, "discord", envOrDefault(notify, func(n *NotifyConfig) string { return n.DiscordWebhookEnv }, "FABRIC_DISCORD_WEBHOOK"), checkEnv)
	appendNotifyStatus(&rep, "teams", envOrDefault(notify, func(n *NotifyConfig) string { return n.TeamsWebhookEnv }, "FABRIC_TEAMS_WEBHOOK"), checkEnv)

	if notify != nil && strings.TrimSpace(notify.WebhookURL) != "" {
		var missing []string
		if notify.WebhookSecretEnv != "" {
			checkEnv(notify.WebhookSecretEnv, &missing)
		}
		rep.Items = append(rep.Items, IntegrationStatus{
			Name:       "outbound-webhook",
			Category:   "notify",
			Configured: true,
			Ready:      len(missing) == 0,
			Endpoint:   notify.WebhookURL,
			MissingEnv: missing,
		})
	} else {
		rep.Items = append(rep.Items, IntegrationStatus{Name: "outbound-webhook", Category: "notify"})
	}

	if cfg != nil && cfg.Webhooks != nil {
		secEnv := firstNonEmpty(cfg.Webhooks.SecretEnv, "FABRIC_WEBHOOK_SECRET")
		var missing []string
		checkEnv(secEnv, &missing)
		rep.Items = append(rep.Items, IntegrationStatus{
			Name:       "inbound-webhook",
			Category:   "webhook",
			Configured: true,
			Ready:      len(missing) == 0,
			MissingEnv: missing,
			Details:    "POST /v1/webhooks/{provider}",
		})
	} else {
		rep.Items = append(rep.Items, IntegrationStatus{Name: "inbound-webhook", Category: "webhook"})
	}

	return rep
}

func appendNotifyStatus(rep *StatusReport, name, envName string, checkEnv func(string, *[]string) bool) {
	if envName == "" {
		rep.Items = append(rep.Items, IntegrationStatus{Name: name, Category: "notify"})
		return
	}
	var missing []string
	checkEnv(envName, &missing)
	rep.Items = append(rep.Items, IntegrationStatus{
		Name:       name,
		Category:   "notify",
		Configured: true,
		Ready:      len(missing) == 0,
		MissingEnv: missing,
		Details:    "env=" + envName,
	})
}

func envOrDefault(n *NotifyConfig, pick func(*NotifyConfig) string, fallback string) string {
	if n != nil {
		if v := strings.TrimSpace(pick(n)); v != "" {
			return v
		}
	}
	if strings.TrimSpace(os.Getenv(fallback)) != "" {
		return fallback
	}
	return ""
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if t := strings.TrimSpace(v); t != "" {
			return t
		}
	}
	return ""
}
