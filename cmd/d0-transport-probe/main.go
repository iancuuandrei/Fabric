package main

import (
	"context"
	"encoding/json"
	"fmt"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
	"harness.local/engorch/internal/codexhost"
	"harness.local/engorch/internal/codexruntime"
	"harness.local/engorch/internal/runtime"
	"harness.local/engorch/internal/writercontract"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	root := os.Args[3]
	if err := os.Mkdir(root, 0700); err != nil {
		panic(err)
	}
	report := map[string]any{"verdict": "FAIL", "candidate_access": false, "provider_invocations": 0}
	started := time.Now()
	defer func() {
		report["wall_ms"] = time.Since(started).Milliseconds()
		b, _ := json.MarshalIndent(report, "", "  ")
		os.WriteFile(filepath.Join(root, "receipt.json"), b, 0600)
		fmt.Println(string(b))
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	if err := os.Mkdir(filepath.Join(root, "host"), 0700); err != nil {
		panic(err)
	}
	launch, err := codexhost.Prepare(filepath.Join(root, "host"), os.Args[1])
	if err != nil {
		report["error"] = err.Error()
		return
	}
	report["launch"] = launch
	host, decision, err := codexhost.StartWithCapabilityConfinement(ctx, launch, codexhost.CapabilityConfinementRequired, nil)
	report["capability_confinement"] = decision
	if err != nil {
		report["error"] = err.Error()
		return
	}
	defer host.Close()
	if err := host.LoginChatGPT(ctx, os.Args[2]); err != nil {
		report["error"] = "authentication failed"
		return
	}
	report["authentication"] = "PASS"
	report["host_receipt"] = host.Receipt
	body := map[string]any{"output_schema": writercontract.UTF8Schema(), "instruction": "ROLE: IMPLEMENTER. Synthetic schema qualification only. Return exactly one change for synthetic.txt, before_hash null, content_utf8 containing exactly hi followed by a newline, executable false, candidate_id " + strings.Repeat("a", 64) + ". This is JSON data only. No tools, reads, commands, or actual file effects."}
	input, _ := json.Marshal(body)
	os.WriteFile(filepath.Join(root, "invocation-input.json"), input, 0600)
	i, err := runtime.NewInvocation(runtime.Profile{Runtime: "codex-app-server", Provider: "openai", Model: "gpt-5.6-sol", Effort: "medium", Role: "writer"}, string(input))
	if err != nil {
		panic(err)
	}
	adapter := &codexruntime.Adapter{Client: host.Client, UsageBudget: 0, UnlimitedTokens: true, RequireLiveUsage: true, UsageQualified: true, JournalPath: filepath.Join(root, "runtime.db"), Directory: filepath.Join(launch.Root, "workspace")}
	report["provider_invocations"] = 1
	if len(os.Args) > 4 && os.Args[4] == "budget-stop" {
		adapter.UnlimitedTokens = false
		adapter.UsageBudget = 1
		adapter.RequireLiveUsage = true
		adapter.UsageQualified = true
	}
	result, err := adapter.Execute(ctx, i)
	if err != nil {
		report["error"] = err.Error()
		state, inspectErr := codexruntime.Inspect(adapter.JournalPath)
		if inspectErr == nil {
			report["runtime_state"] = state
			if state.UsageFailure == "BUDGET_EXHAUSTED" {
				report["verdict"] = "R3_LIVE_BUDGET_STOP_OBSERVED"
			}
		}
		return
	}
	report["result"] = result
	os.WriteFile(filepath.Join(root, "raw-output.json"), []byte(result.Output), 0600)
	state, err := codexruntime.Inspect(adapter.JournalPath)
	if err != nil {
		report["error"] = err.Error()
		return
	}
	report["route_evidence"] = state.RouteEvidence
	report["execution_outcome"] = state.ExecutionOutcome
	var schema, value any
	json.Unmarshal(writercontract.UTF8Schema(), &schema)
	if err := json.Unmarshal([]byte(result.Output), &value); err != nil {
		report["error"] = err.Error()
		return
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	if err := c.AddResource("urn:writer", schema); err != nil {
		panic(err)
	}
	compiled, err := c.Compile("urn:writer")
	if err != nil {
		panic(err)
	}
	if err := compiled.Validate(value); err != nil {
		report["error"] = err.Error()
		return
	}
	if result.ObservedModel == nil || *result.ObservedModel != "gpt-5.6-sol" || result.ObservedEffort == nil || *result.ObservedEffort != "medium" || !state.NotificationStreamComplete {
		report["error"] = "route or terminal proof missing"
		return
	}
	if result.Usage.Accounting == nil || result.Usage.Accounting.Coverage != "OBSERVED" {
		report["error"] = "LIVE_USAGE_MISSING"
		return
	}
	report["verdict"] = "R5_UTF8_TRANSPORT_LIVE_QUALIFIED"
}
