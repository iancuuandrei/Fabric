package integrations

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
)

// Notification describes one completed or blocked Fabric run event formatted
// for Slack, Discord, Microsoft Teams, or HMAC-signed generic webhooks.
type Notification struct {
	Event        string   `json:"event"`
	Status       string   `json:"status"`
	RunID        string   `json:"run_id"`
	Objective    string   `json:"objective"`
	Summary      string   `json:"summary,omitempty"`
	TicketKey    string   `json:"ticket_key,omitempty"`
	TicketURL    string   `json:"ticket_url,omitempty"`
	PRURL        string   `json:"pr_url,omitempty"`
	ChangedFiles []string `json:"changed_files,omitempty"`
	DiffAdded    int      `json:"diff_added"`
	DiffRemoved  int      `json:"diff_removed"`
	CostUSD      float64  `json:"cost_usd"`
	MutationKill string   `json:"mutation_kill,omitempty"`
}

// DeliveryResult records the delivery status for one configured notification channel.
type DeliveryResult struct {
	Channel    string `json:"channel"`
	Sent       bool   `json:"sent"`
	Skipped    bool   `json:"skipped,omitempty"`
	StatusCode int    `json:"status_code,omitempty"`
	Error      string `json:"error,omitempty"`
}

// EventNameForStatus normalizes an episode status into an event name.
func EventNameForStatus(status string) string {
	s := strings.ToLower(strings.TrimSpace(status))
	if s == "" {
		return "episode.completed"
	}
	return "episode." + s
}

// ShouldNotify reports whether cfg authorizes automatic notifications for status.
func ShouldNotify(cfg *NotifyConfig, status string) bool {
	if cfg == nil {
		return false
	}
	hasTarget := strings.TrimSpace(cfg.WebhookURL) != "" ||
		strings.TrimSpace(cfg.SlackWebhookEnv) != "" ||
		strings.TrimSpace(cfg.DiscordWebhookEnv) != "" ||
		strings.TrimSpace(cfg.TeamsWebhookEnv) != ""
	if !hasTarget {
		return false
	}
	if len(cfg.Events) == 0 {
		return true
	}
	for _, want := range cfg.Events {
		if strings.EqualFold(strings.TrimSpace(want), strings.TrimSpace(status)) || strings.TrimSpace(want) == "*" {
			return true
		}
	}
	return false
}

// NotifyAll sends n to every configured channel (or one specific channelFilter such as "slack").
func NotifyAll(ctx context.Context, cfg *NotifyConfig, n Notification, channelFilter string, client *http.Client) []DeliveryResult {
	if strings.TrimSpace(n.Event) == "" {
		n.Event = EventNameForStatus(n.Status)
	}
	filter := strings.ToLower(strings.TrimSpace(channelFilter))
	if filter == "" {
		filter = "all"
	}
	match := func(name string) bool { return filter == "all" || filter == name }

	var results []DeliveryResult
	if match("slack") {
		if urlStr := resolveWebhookEnv(cfg, func(c *NotifyConfig) string { return c.SlackWebhookEnv }, "FABRIC_SLACK_WEBHOOK"); urlStr != "" {
			results = append(results, postNotification(ctx, client, "slack", urlStr, nil, slackPayload(n)))
		}
	}
	if match("discord") {
		if urlStr := resolveWebhookEnv(cfg, func(c *NotifyConfig) string { return c.DiscordWebhookEnv }, "FABRIC_DISCORD_WEBHOOK"); urlStr != "" {
			results = append(results, postNotification(ctx, client, "discord", urlStr, nil, discordPayload(n)))
		}
	}
	if match("teams") {
		if urlStr := resolveWebhookEnv(cfg, func(c *NotifyConfig) string { return c.TeamsWebhookEnv }, "FABRIC_TEAMS_WEBHOOK"); urlStr != "" {
			results = append(results, postNotification(ctx, client, "teams", urlStr, nil, teamsPayload(n)))
		}
	}
	if match("webhook") && cfg != nil && strings.TrimSpace(cfg.WebhookURL) != "" {
		raw, err := json.Marshal(n)
		if err != nil {
			results = append(results, DeliveryResult{Channel: "webhook", Error: err.Error()})
		} else {
			hdrs := map[string]string{}
			if secEnv := strings.TrimSpace(cfg.WebhookSecretEnv); secEnv != "" {
				if secret := strings.TrimSpace(os.Getenv(secEnv)); secret != "" {
					mac := hmac.New(sha256.New, []byte(secret))
					_, _ = mac.Write(raw)
					hdrs["X-Fabric-Signature-256"] = "sha256=" + hex.EncodeToString(mac.Sum(nil))
				}
			}
			results = append(results, postNotification(ctx, client, "webhook", cfg.WebhookURL, hdrs, json.RawMessage(raw)))
		}
	}
	return results
}

func resolveWebhookEnv(cfg *NotifyConfig, pick func(*NotifyConfig) string, fallbackEnv string) string {
	var envName string
	if cfg != nil {
		envName = strings.TrimSpace(pick(cfg))
	}
	if envName == "" {
		envName = fallbackEnv
	}
	return strings.TrimSpace(os.Getenv(envName))
}

func postNotification(ctx context.Context, client *http.Client, channel, endpoint string, headers map[string]string, payload any) DeliveryResult {
	code, raw, err := doJSON(ctx, client, http.MethodPost, endpoint, headers, payload, nil)
	if err != nil {
		return DeliveryResult{Channel: channel, StatusCode: code, Error: err.Error()}
	}
	if code < 200 || code >= 300 {
		return DeliveryResult{Channel: channel, StatusCode: code, Error: httpError(channel, code, raw).Error()}
	}
	return DeliveryResult{Channel: channel, Sent: true, StatusCode: code}
}

func headlineText(n Notification) string {
	short := n.RunID
	if len(short) > 10 {
		short = short[:10]
	}
	title := fmt.Sprintf("Fabric [%s] · %s", n.Status, short)
	if n.TicketKey != "" {
		title += " · " + n.TicketKey
	}
	return title
}

func slackPayload(n Notification) map[string]any {
	blocks := []map[string]any{
		{
			"type": "header",
			"text": map[string]string{"type": "plain_text", "text": headlineText(n)},
		},
		{
			"type": "section",
			"text": map[string]string{"type": "mrkdwn", "text": fmt.Sprintf("*Objective:* %s", truncateText(n.Objective, 300))},
			"fields": []map[string]string{
				{"type": "mrkdwn", "text": fmt.Sprintf("*Diff Churn:*\n+%d / -%d (%d files)", n.DiffAdded, n.DiffRemoved, len(n.ChangedFiles))},
				{"type": "mrkdwn", "text": fmt.Sprintf("*Spend / Mutation:*\n$%.4f · %s", n.CostUSD, firstNonEmpty(n.MutationKill, "n/a"))},
			},
		},
	}
	if n.Summary != "" || n.PRURL != "" || n.TicketURL != "" {
		var lines []string
		if n.Summary != "" {
			lines = append(lines, truncateText(n.Summary, 400))
		}
		if n.TicketURL != "" {
			lines = append(lines, fmt.Sprintf("<%s|Ticket %s>", n.TicketURL, firstNonEmpty(n.TicketKey, "link")))
		}
		if n.PRURL != "" {
			lines = append(lines, fmt.Sprintf("<%s|Pull Request>", n.PRURL))
		}
		blocks = append(blocks, map[string]any{
			"type": "context",
			"elements": []map[string]string{
				{"type": "mrkdwn", "text": strings.Join(lines, " · ")},
			},
		})
	}
	return map[string]any{"text": headlineText(n), "blocks": blocks}
}

func discordPayload(n Notification) map[string]any {
	color := 0xf59e0b
	switch strings.ToUpper(n.Status) {
	case "READY":
		color = 0x22c55e
	case "FAILED", "BUDGET_EXCEEDED", "CANCELLED":
		color = 0xef4444
	}
	desc := truncateText(n.Objective, 300)
	if n.Summary != "" {
		desc += "\n\n" + truncateText(n.Summary, 400)
	}
	if n.PRURL != "" {
		desc += "\nPR: " + n.PRURL
	}
	return map[string]any{
		"embeds": []map[string]any{
			{
				"title":       headlineText(n),
				"description": desc,
				"color":       color,
				"fields": []map[string]any{
					{"name": "Churn", "value": fmt.Sprintf("+%d / -%d (%d files)", n.DiffAdded, n.DiffRemoved, len(n.ChangedFiles)), "inline": true},
					{"name": "Cost", "value": fmt.Sprintf("$%.4f", n.CostUSD), "inline": true},
					{"name": "Mutation", "value": firstNonEmpty(n.MutationKill, "not run"), "inline": true},
				},
			},
		},
	}
}

func teamsPayload(n Notification) map[string]any {
	color := "f59e0b"
	switch strings.ToUpper(n.Status) {
	case "READY":
		color = "22c55e"
	case "FAILED", "BUDGET_EXCEEDED", "CANCELLED":
		color = "ef4444"
	}
	facts := []map[string]string{
		{"name": "Status", "value": n.Status},
		{"name": "Churn", "value": fmt.Sprintf("+%d / -%d (%d files)", n.DiffAdded, n.DiffRemoved, len(n.ChangedFiles))},
		{"name": "Cost", "value": fmt.Sprintf("$%.4f", n.CostUSD)},
		{"name": "Mutation", "value": firstNonEmpty(n.MutationKill, "not run")},
	}
	if n.PRURL != "" {
		facts = append(facts, map[string]string{"name": "PR", "value": n.PRURL})
	}
	return map[string]any{
		"@type":      "MessageCard",
		"@context":   "http://schema.org/extensions",
		"themeColor": color,
		"summary":    headlineText(n),
		"title":      headlineText(n),
		"sections": []map[string]any{
			{
				"activityTitle": truncateText(n.Objective, 250),
				"text":          truncateText(n.Summary, 400),
				"facts":         facts,
			},
		},
	}
}

func truncateText(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
