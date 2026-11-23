package agentloop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/iancuuandrei/fabric/internal/clauderuntime"
	"github.com/iancuuandrei/fabric/internal/config"
	"github.com/iancuuandrei/fabric/internal/cost"
	"github.com/iancuuandrei/fabric/internal/directapi"
)

// ModelOverrides lets CLI or API callers override the model backend declared in fabric.toml.
type ModelOverrides struct {
	Dialect   string
	BaseURL   string
	APIKeyEnv string
	Model     string
	ClaudeBin string
}

// LoadRepoConfig loads fabric.toml (or legacy harness.toml) from repoRoot.
func LoadRepoConfig(repoRoot string) (config.Config, error) {
	candidates := []string{
		filepath.Join(repoRoot, "fabric.toml"),
		filepath.Join(repoRoot, "harness.toml"),
	}
	for _, p := range candidates {
		raw, err := os.ReadFile(p)
		if err == nil {
			return config.Parse(raw)
		}
		if !os.IsNotExist(err) {
			return config.Config{}, err
		}
	}
	return config.Config{}, errors.New("neither fabric.toml nor harness.toml found; run `fabric init --auto` first")
}

// ModelFromConfig constructs an agentloop Model from explicit overrides,
// repository configuration, or detected environment credentials.
func ModelFromConfig(cfg *config.Config, ov ModelOverrides) (Model, error) {
	modelName := strings.TrimSpace(ov.Model)
	if modelName == "" && cfg != nil {
		if cfg.Writer != nil && strings.TrimSpace(cfg.Writer.Model) != "" {
			modelName = cfg.Writer.Model
		} else if strings.TrimSpace(cfg.Planner.Model) != "" {
			modelName = cfg.Planner.Model
		}
	}

	// 1. Explicit HTTP dialect override.
	if d := strings.ToLower(strings.TrimSpace(ov.Dialect)); d != "" {
		if modelName == "" {
			if d == directapi.DialectAnthropic {
				modelName = "claude-sonnet-4-5"
			} else {
				modelName = "gpt-5"
			}
		}
		return &HTTPModel{
			Dialect:   d,
			BaseURL:   ov.BaseURL,
			APIKeyEnv: ov.APIKeyEnv,
			ModelName: modelName,
		}, nil
	}

	// 2. Explicit Claude CLI binary override.
	if strings.TrimSpace(ov.ClaudeBin) != "" {
		if modelName == "" {
			modelName = "claude-sonnet-4-5"
		}
		return &ClaudeCLIModel{Executable: ov.ClaudeBin, ModelName: modelName}, nil
	}

	// 3. Configured DirectAPI in fabric.toml.
	if cfg != nil && cfg.DirectAPI != nil {
		if modelName == "" {
			if cfg.DirectAPI.Dialect == directapi.DialectAnthropic {
				modelName = "claude-sonnet-4-5"
			} else {
				modelName = "gpt-5"
			}
		}
		baseURL := ov.BaseURL
		if baseURL == "" {
			baseURL = cfg.DirectAPI.BaseURL
		}
		keyEnv := ov.APIKeyEnv
		if keyEnv == "" {
			keyEnv = cfg.DirectAPI.APIKeyEnv
		}
		return &HTTPModel{
			Dialect:         cfg.DirectAPI.Dialect,
			BaseURL:         baseURL,
			APIKeyEnv:       keyEnv,
			ModelName:       modelName,
			MaxOutputTokens: cfg.DirectAPI.MaxOutputTokens,
		}, nil
	}

	// 4. Configured Claude CLI in fabric.toml.
	if cfg != nil && cfg.Claude != nil && cfg.Claude.Executable != "" {
		if modelName == "" {
			modelName = "claude-sonnet-4-5"
		}
		return &ClaudeCLIModel{
			Executable: cfg.Claude.Executable,
			ModelName:  modelName,
			ExtraArgs:  cfg.Claude.ExtraArgs,
		}, nil
	}

	// 5. Environment auto-detection fallback (API keys -> Claude CLI).
	if os.Getenv("ANTHROPIC_API_KEY") != "" {
		if modelName == "" {
			modelName = "claude-sonnet-4-5"
		}
		return &HTTPModel{Dialect: directapi.DialectAnthropic, ModelName: modelName}, nil
	}
	if os.Getenv("OPENAI_API_KEY") != "" {
		if modelName == "" {
			modelName = "gpt-5"
		}
		return &HTTPModel{Dialect: directapi.DialectOpenAI, ModelName: modelName}, nil
	}
	if exe, _, err := clauderuntime.DiscoverExecutable(); err == nil {
		if modelName == "" {
			modelName = "claude-sonnet-4-5"
		}
		return &ClaudeCLIModel{Executable: exe, ModelName: modelName}, nil
	}
	return nil, errors.New("no model backend configured: set --api anthropic|openai, configure [direct_api] or [claude] in fabric.toml, or export ANTHROPIC_API_KEY / OPENAI_API_KEY")
}

// ClaudeCLIModel adapts the local Claude Code CLI (in read-only toolless plan
// mode) into the agentloop.Model interface so Fabric controls every file read,
// anchored edit, checkpoint, and command execution deterministically.
type ClaudeCLIModel struct {
	Executable string
	ModelName  string
	ExtraArgs  []string
	Timeout    time.Duration
}

// Name returns the configured Claude model identifier.
func (m *ClaudeCLIModel) Name() string { return m.ModelName }

// Complete runs one headless, tool-disabled Claude CLI turn using a strict JSON
// tool-calling envelope so Fabric executes all mutations inside its worktree.
func (m *ClaudeCLIModel) Complete(ctx context.Context, system string, messages []Message, tools []ToolDef) (ModelResponse, error) {
	timeout := m.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var prompt strings.Builder
	prompt.WriteString(system)
	prompt.WriteString("\n\n")
	if len(tools) > 0 {
		tb, _ := json.Marshal(tools)
		prompt.WriteString("Available Fabric tools (JSON schema):\n")
		prompt.Write(tb)
		prompt.WriteString("\n\nRespond strictly with a single JSON object (no prose outside JSON):\n")
		prompt.WriteString("- To invoke tools: {\"thought\":\"short reasoning\",\"tool_calls\":[{\"id\":\"c1\",\"name\":\"read_file\",\"arguments\":{\"path\":\"...\"}}]}\n")
		prompt.WriteString("- When finished and ready for Fabric verification: {\"done\":true,\"summary\":\"concise summary of changes\"}\n\n")
	}
	prompt.WriteString("Conversation history:\n")
	for _, msg := range messages {
		mb, _ := json.Marshal(msg)
		prompt.Write(mb)
		prompt.WriteByte('\n')
	}

	args := []string{
		"-p", prompt.String(),
		"--output-format", "json",
		"--permission-mode", "plan",
		"--tools", "",
	}
	if strings.TrimSpace(m.ModelName) != "" {
		args = append(args, "--model", m.ModelName)
	}
	args = append(args, m.ExtraArgs...)

	cmd := exec.CommandContext(runCtx, m.Executable, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return ModelResponse{}, fmt.Errorf("claude-cli failed: %w: %s", err, strings.TrimSpace(string(out[:min(len(out), 512)])))
	}

	// Parse Claude CLI JSON envelope.
	var env struct {
		Result string `json:"result"`
		Usage  struct {
			InputTokens              int64 `json:"input_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
		} `json:"usage"`
	}
	rawText := strings.TrimSpace(string(out))
	usage := cost.Usage{}
	if json.Unmarshal(out, &env) == nil && env.Result != "" {
		rawText = strings.TrimSpace(env.Result)
		usage = cost.Usage{
			InputTokens:      env.Usage.InputTokens + env.Usage.CacheCreationInputTokens + env.Usage.CacheReadInputTokens,
			CachedTokens:     env.Usage.CacheReadInputTokens,
			CacheWriteTokens: env.Usage.CacheCreationInputTokens,
			OutputTokens:     env.Usage.OutputTokens,
		}
	}

	// Unwrap optional fenced JSON block.
	cleaned := rawText
	if strings.HasPrefix(cleaned, "```") {
		if nl := strings.IndexByte(cleaned, '\n'); nl >= 0 {
			inner := cleaned[nl+1:]
			if end := strings.LastIndex(inner, "```"); end >= 0 {
				cleaned = strings.TrimSpace(inner[:end])
			}
		}
	}

	if len(tools) == 0 {
		return ModelResponse{Text: cleaned, Usage: usage, Stop: "end_turn"}, nil
	}

	var parsed struct {
		Thought   string     `json:"thought"`
		Done      bool       `json:"done"`
		Summary   string     `json:"summary"`
		ToolCalls []ToolCall `json:"tool_calls"`
	}
	if err := json.Unmarshal([]byte(cleaned), &parsed); err != nil {
		// Treat plain text as a final answer if not JSON tool call.
		return ModelResponse{Text: rawText, Usage: usage, Stop: "end_turn"}, nil
	}
	if parsed.Done || len(parsed.ToolCalls) == 0 {
		txt := parsed.Summary
		if txt == "" {
			txt = parsed.Thought
		}
		return ModelResponse{Text: txt, Usage: usage, Stop: "end_turn"}, nil
	}
	for i := range parsed.ToolCalls {
		if parsed.ToolCalls[i].ID == "" {
			parsed.ToolCalls[i].ID = fmt.Sprintf("claude-%d", i+1)
		}
	}
	return ModelResponse{
		Text:      parsed.Thought,
		ToolCalls: parsed.ToolCalls,
		Usage:     usage,
		Stop:      "tool_use",
	}, nil
}
