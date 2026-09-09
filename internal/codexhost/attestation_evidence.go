package codexhost

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/runtime"
	"harness.local/engorch/internal/safepath"
)

type qualificationCase struct {
	Name             string   `json:"name"`
	ResultFile       string   `json:"result_file"`
	RPCInputFile     string   `json:"rpc_input_file"`
	RPCOutputFile    string   `json:"rpc_output_file"`
	RequestFiles     []string `json:"request_files"`
	RolloutFiles     []string `json:"rollout_files"`
	DynamicToolsFile string   `json:"dynamic_tools_file,omitempty"`
}

type qualificationProof struct {
	Version int                 `json:"version"`
	Cases   []qualificationCase `json:"cases"`
}

var requiredQualificationCases = []string{"final-case-e", "final-case-e-reviewer", "final-clean", "final-ns-faulthook", "final-ns-nohook", "final-plain-faulthook", "final-plain-nohook", "final-search-functions", "final-search-native", "final-search-plain"}

// fakeProviderForModel is the finite diagnostic provider map used by the
// offline qualification captures. An unknown model must never acquire a
// provider by convention or fallback.
func fakeProviderForModel(model string) (string, error) {
	switch model {
	case "gpt-5.6-sol":
		return "r17-loopback", nil
	case "gpt-5.6-luna":
		return "luna-q1-loopback", nil
	default:
		return "", errors.New("unqualified Codex model")
	}
}

// selectLeafProfile validates the equal-model planner/reviewer pair in one
// v1 leaf and selects the exact caller profile plus dynamic-tool identity.
// R17 evidence is retained as a v1 leaf format; its model is deliberately
// derived from this pair so the same strict proof can qualify Luna.
func selectLeafProfile(m attestationManifest, selected runtime.Profile, toolsHash string) (runtime.Profile, string, error) {
	if len(m.Profiles) != 2 {
		return runtime.Profile{}, "", errors.New("R17 qualifies exactly planner and reviewer profiles")
	}
	seenRoles := map[string]bool{}
	var model, effort string
	matched := 0
	for _, entry := range m.Profiles {
		p := entry.Profile
		if p.Validate() != nil || p.Runtime != "codex-app-server" || p.Provider != "openai" || (p.Role != "planner" && p.Role != "reviewer") || seenRoles[p.Role] || safepath.RequireDigest(entry.DynamicToolsSHA256) != nil || safepath.RequireDigest(entry.PreparedToolInventorySHA256) != nil {
			return runtime.Profile{}, "", errors.New("invalid attested profile entry")
		}
		seenRoles[p.Role] = true
		if model == "" {
			model, effort = p.Model, p.Effort
		} else if p.Model != model || p.Effort != effort {
			return runtime.Profile{}, "", errors.New("planner and reviewer models differ")
		}
		if p == selected && entry.DynamicToolsSHA256 == toolsHash {
			matched++
		}
	}
	if !seenRoles["planner"] || !seenRoles["reviewer"] || matched != 1 {
		return runtime.Profile{}, "", errors.New("profile and dynamic tools are not attested")
	}
	provider, err := fakeProviderForModel(model)
	if err != nil {
		return runtime.Profile{}, "", err
	}
	return selected, provider, nil
}

// proofBytes only reads files explicitly pinned by the controller's manifest.
// It rechecks their exact bytes at use, rather than trusting a PASS field.
func proofBytes(root *os.Root, m attestationManifest, path string) ([]byte, error) {
	if safepath.Relative(path) != nil {
		return nil, errors.New("invalid proof path")
	}
	for _, e := range m.Evidence {
		if e.Path == path {
			var b []byte
			h, n, _, exists, err := safepath.CopyRegular(root, path, 16<<20, sliceWriter{bytes: &b})
			if err != nil || !exists || h != e.SHA256 || n != e.Bytes {
				return nil, errors.New("proof bytes changed")
			}
			return b, nil
		}
	}
	return nil, errors.New("proof file not pinned")
}

func decodeProvider(b []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if err := d.Decode(v); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return errors.New("trailing provider JSON")
	}
	return nil
}

func proofLines(b []byte) ([]map[string]any, error) {
	var out []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(b), []byte{'\n'}) {
		var item map[string]any
		if err := decodeProvider(line, &item); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, nil
}
func object(v any) map[string]any { m, _ := v.(map[string]any); return m }
func array(v any) []any           { a, _ := v.([]any); return a }
func stringValue(v any) string    { s, _ := v.(string); return s }

func validateQualification(root *os.Root, m attestationManifest, profile runtime.Profile, fakeProvider, metadataHash string) error {
	raw, err := proofBytes(root, m, m.QualificationFile)
	if err != nil {
		return err
	}
	var proof qualificationProof
	if err = canonical.Decode(raw, &proof); err != nil {
		return err
	}
	if proof.Version != 1 || len(proof.Cases) != len(requiredQualificationCases) {
		return errors.New("incomplete registry qualification")
	}
	seen := map[string]bool{}
	for _, c := range proof.Cases {
		if !slices.Contains(requiredQualificationCases, c.Name) || seen[c.Name] {
			return errors.New("unknown or duplicate qualification case")
		}
		seen[c.Name] = true
		if err = validateQualificationCase(root, m, c, profile, fakeProvider, metadataHash); err != nil {
			return fmt.Errorf("registry proof %s: %w", c.Name, err)
		}
	}
	catalogBytes, err := proofBytes(root, m, m.CatalogFile)
	if err != nil {
		return err
	}
	var catalog struct {
		Models []map[string]any `json:"models"`
	}
	if err = decodeProvider(catalogBytes, &catalog); err != nil {
		return err
	}
	selected := 0
	for _, record := range catalog.Models {
		if record["slug"] == profile.Model {
			selected++
			h, err := SelectedModelRecordHash(record)
			metadata, metadataErr := SelectedModelMetadataHash(record)
			if err != nil || metadataErr != nil || h != m.SelectedModelRecordSHA256 || metadata != metadataHash || !catalogSupportsEffort(record, profile.Effort) {
				return errors.New("selected model record differs")
			}
		}
	}
	if selected != 1 {
		return errors.New("selected model missing or duplicated")
	}
	return nil
}

func catalogSupportsEffort(record map[string]any, effort string) bool {
	for _, item := range array(record["supported_reasoning_levels"]) {
		if object(item)["effort"] == effort {
			return true
		}
	}
	return false
}

// validateCapturedIdentity walks all raw provider/result/RPC/request objects.
// Exact identity fields are checked wherever they occur, including nested
// thread settings and thread listings. transport_deltas.model_provider is a
// historical diagnostic label and is intentionally handled by its own probe
// provenance, so it is not confused with a requested model-provider field.
func validateCapturedIdentity(value any, profile runtime.Profile, fakeProvider string) error {
	var walk func(any) error
	walk = func(current any) error {
		switch item := current.(type) {
		case map[string]any:
			for key, child := range item {
				switch key {
				case "model", "requested_model":
					if child != profile.Model {
						return errors.New("captured model differs")
					}
				case "modelProvider", "requested_provider":
					if child != fakeProvider {
						return errors.New("captured provider differs")
					}
				case "reasoningEffort", "reasoning_effort", "requested_effort":
					if child != profile.Effort {
						return errors.New("captured effort differs")
					}
				}
				if err := walk(child); err != nil {
					return err
				}
			}
		case []any:
			for _, child := range item {
				if err := walk(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(value)
}

func validateQualificationCase(root *os.Root, m attestationManifest, c qualificationCase, profile runtime.Profile, fakeProvider, metadataHash string) error {
	fail := func() error { return errors.New("raw qualification evidence mismatch") }
	raw, err := proofBytes(root, m, c.ResultFile)
	if err != nil {
		return err
	}
	var r map[string]any
	if err = decodeProvider(raw, &r); err != nil {
		return err
	}
	if err = validateCapturedIdentity(r, profile, fakeProvider); err != nil {
		return fail()
	}
	if r["status"] != "CAPTURED" || r["binary_sha256"] != m.BinarySHA256 || r["copied_catalog_sha256"] != m.CatalogSHA256 || r["selected_model_metadata_sha256"] != metadataHash || r["agents_enabled"] != false || r["http_failure"] != nil {
		return fail()
	}
	command := array(r["command"])
	args := launchArguments()
	if len(command) != len(args)+1 || command[0] != r["binary"] || command[1] != "app-server" || command[2] != "--stdio" || command[3] != "--strict-config" {
		return fail()
	}
	observedOverrides := map[string]string{}
	for index := 4; index+1 < len(command); index += 2 {
		if command[index] != "-c" {
			return fail()
		}
		{
			key, value, ok := strings.Cut(stringValue(command[index+1]), "=")
			if !ok || key == "" {
				return fail()
			}
			if _, exists := observedOverrides[key]; exists {
				return fail()
			}
			observedOverrides[key] = value
		}
	}
	for index := 0; index+1 < len(args); index++ {
		if args[index] == "-c" {
			key, value, ok := strings.Cut(args[index+1], "=")
			if !ok {
				return fail()
			}
			if key == "features.hooks" && strings.Contains(c.Name, "faulthook") {
				value = "true"
			}
			if observedOverrides[key] != value {
				return fail()
			}
		}
	}
	// The profile is the actual fixed launcher, with only documented diagnostic
	// provider/catalog and hook controls differing from production.
	for _, flag := range []string{"agents.enabled=false", "features.multi_agent=false", "features.multi_agent_v2=false"} {
		if !slices.Contains(command, any(flag)) {
			return fail()
		}
	}
	initialized := object(object(r["initialize"])["result"])
	if initialized["userAgent"] != m.CLIIdentity {
		return fail()
	}
	thread := object(object(r["thread_start"])["result"])
	threadID := stringValue(object(thread["thread"])["id"])
	turnID := stringValue(object(object(object(r["turn_start"])["result"])["turn"])["id"])
	if threadID == "" || turnID == "" || thread["model"] != profile.Model || thread["modelProvider"] != fakeProvider || thread["reasoningEffort"] != profile.Effort {
		return fail()
	}
	listed := object(object(r["thread_list_after_turn"])["result"])
	if len(array(listed["data"])) != 1 || object(array(listed["data"])[0])["id"] != threadID || listed["nextCursor"] != nil {
		return fail()
	}
	if len(c.RolloutFiles) != 1 {
		return fail()
	}
	rollout, err := proofBytes(root, m, c.RolloutFiles[0])
	if err != nil {
		return err
	}
	lines, err := proofLines(rollout)
	if err != nil {
		return err
	}
	metadata := 0
	for _, line := range lines {
		if line["type"] == "session_meta" {
			metadata++
			p := object(line["payload"])
			if p["id"] != threadID || p["parent_thread_id"] != nil {
				return fail()
			}
		}
	}
	if metadata != 1 {
		return fail()
	}
	inRaw, err := proofBytes(root, m, c.RPCInputFile)
	if err != nil {
		return err
	}
	in, err := proofLines(inRaw)
	if err != nil {
		return err
	}
	outRaw, err := proofBytes(root, m, c.RPCOutputFile)
	if err != nil {
		return err
	}
	out, err := proofLines(outRaw)
	if err != nil {
		return err
	}
	for _, line := range append(append([]map[string]any{}, in...), out...) {
		if err := validateCapturedIdentity(line, profile, fakeProvider); err != nil {
			return fail()
		}
	}
	starts, turns, terminals, usage, dynamic := 0, 0, 0, 0, 0
	for _, call := range in {
		p := object(call["params"])
		switch call["method"] {
		case "thread/start":
			starts++
			cfg := object(p["config"])
			if p["model"] != profile.Model || p["modelProvider"] != fakeProvider || p["allowProviderModelFallback"] != false {
				return fail()
			}
			for _, flag := range []string{"agents.enabled", "features.multi_agent", "features.multi_agent_v2"} {
				if cfg[flag] != false {
					return fail()
				}
			}
		case "turn/start":
			turns++
			if p["threadId"] != threadID || p["model"] != profile.Model || p["effort"] != profile.Effort {
				return fail()
			}
		}
	}
	for _, event := range out {
		p := object(event["params"])
		method := stringValue(event["method"])
		if strings.Contains(strings.ToLower(method), "subagent") || strings.HasPrefix(method, "hook/") {
			return fail()
		}
		switch method {
		case "thread/started":
			if object(p["thread"])["id"] != threadID {
				return fail()
			}
		case "turn/completed":
			terminals++
			if p["threadId"] != threadID || object(p["turn"])["id"] != turnID || object(p["turn"])["status"] != "completed" {
				return fail()
			}
		case "thread/tokenUsage/updated":
			usage++
			if p["threadId"] != threadID || p["turnId"] != turnID {
				return fail()
			}
		case "item/tool/call":
			dynamic++
			if p["threadId"] != threadID || p["turnId"] != turnID || p["tool"] != "source_list" {
				return fail()
			}
			answered := 0
			for _, response := range in {
				if response["method"] == nil && response["id"] == event["id"] && object(response["result"])["success"] == true {
					answered++
				}
			}
			if answered != 1 {
				return fail()
			}
		}
	}
	wantRequests := 2
	if c.Name == "final-clean" {
		wantRequests = 1
	}
	if strings.Contains(c.Name, "case-e") {
		wantRequests = 3
	}
	if starts != 1 || turns != 1 || terminals != 1 || usage != wantRequests || len(c.RequestFiles) != wantRequests {
		return fail()
	}
	if strings.Contains(c.Name, "faulthook") {
		if r["hook_mode"] != "fault" || r["hook_matcher"] != ".*" {
			return fail()
		}
		data := array(object(object(r["hooks_list"])["result"])["data"])
		if len(data) != 1 {
			return fail()
		}
		hooks := array(object(data[0])["hooks"])
		if len(hooks) != 1 || object(hooks[0])["enabled"] != true || len(array(object(data[0])["errors"])) != 0 {
			return fail()
		}
	} else if r["hook_mode"] != nil {
		return fail()
	}
	var role *attestedProfile
	names := []string{"functions.exec", "functions.wait", "functions.request_user_input"}
	if strings.Contains(c.Name, "case-e") {
		if dynamic != 1 {
			return fail()
		}
		requestedRole := "planner"
		if c.Name == "final-case-e-reviewer" {
			requestedRole = "reviewer"
		}
		for index := range m.Profiles {
			entry := &m.Profiles[index]
			// Each leaf proves both roles. The caller selects one exact profile
			// and tool set at the leaf boundary, while this case exercises the
			// corresponding role's dynamic inventory. Do not require the
			// caller's role here: the reviewer case is deliberately part of the
			// planner leaf's complete ten-case qualification set (and vice versa).
			if entry.Profile.Role == requestedRole && entry.Profile.Provider == "openai" && entry.Profile.Runtime == "codex-app-server" && entry.Profile.Model == profile.Model && entry.Profile.Effort == profile.Effort {
				if role != nil {
					return fail()
				}
				role = entry
			}
		}
		if role == nil {
			return fail()
		}
		toolRaw, err := proofBytes(root, m, c.DynamicToolsFile)
		if err != nil {
			return err
		}
		var tools []any
		if err = decodeProvider(toolRaw, &tools); err != nil {
			return err
		}
		h, err := DynamicToolsHash(tools)
		if err != nil || h != role.DynamicToolsSHA256 {
			return fail()
		}
		for _, tool := range tools {
			names = append(names, "functions."+stringValue(object(tool)["name"]))
		}
	} else if dynamic != 0 || c.DynamicToolsFile != "" {
		return fail()
	}
	outputs := []string{}
	nativeSearch := false
	nativeSearchCall := ""
	nativeSearchResult := ""
	for index, path := range c.RequestFiles {
		bodyRaw, err := proofBytes(root, m, path)
		if err != nil {
			return err
		}
		var body map[string]any
		if err = decodeProvider(bodyRaw, &body); err != nil {
			return err
		}
		if body["model"] != profile.Model || object(body["reasoning"])["effort"] != profile.Effort {
			return fail()
		}
		specs := array(body["tools"])
		for _, v := range array(body["input"]) {
			item := object(v)
			if item["type"] == "tool_search_call" {
				arguments := object(item["arguments"])
				if c.Name != "final-search-native" || arguments["query"] != "spawn_agent collaboration" || arguments["limit"] != json.Number("10") || item["execution"] != "client" {
					return fail()
				}
				nativeSearchCall = stringValue(item["call_id"])
			}
			if item["type"] == "additional_tools" {
				specs = append(specs, array(item["tools"])...)
			}
			if index == wantRequests-1 && item["type"] == "function_call_output" {
				outputs = append(outputs, stringValue(item["output"]))
			}
			if item["type"] == "tool_search_output" {
				if c.Name != "final-search-native" || item["status"] != "completed" || len(array(item["tools"])) != 0 {
					return fail()
				}
				nativeSearch = true
				nativeSearchResult = stringValue(item["call_id"])
			}
		}
		inventory, err := inventoryNames(specs, "")
		if err != nil {
			return err
		}
		got, err := PreparedToolInventoryHash(inventory)
		if err != nil {
			return err
		}
		expected, err := PreparedToolInventoryHash(names)
		if err != nil || got != expected {
			return fail()
		}
		if role != nil && got != role.PreparedToolInventorySHA256 {
			return fail()
		}
	}
	switch {
	case c.Name == "final-clean":
		if len(outputs) != 0 {
			return fail()
		}
	case c.Name == "final-search-native":
		if !nativeSearch || nativeSearchCall == "" || nativeSearchCall != nativeSearchResult {
			return fail()
		}
	case strings.Contains(c.Name, "case-e"):
		if !slices.Equal(outputs, []string{"{\"entries\":[],\"next_after\":null}", "unsupported call: collaborationspawn_agent"}) {
			return fail()
		}
	default:
		expected := "unsupported call: spawn_agent"
		if strings.Contains(c.Name, "-ns-") {
			expected = "unsupported call: collaborationspawn_agent"
		}
		if strings.Contains(c.Name, "search") {
			expected = "unsupported call: tool_search"
		}
		if !slices.Equal(outputs, []string{expected}) {
			return fail()
		}
	}
	return nil
}

func inventoryNames(specs []any, namespace string) ([]string, error) {
	var names []string
	for _, spec := range specs {
		item := object(spec)
		name := stringValue(item["name"])
		if name == "" {
			return nil, errors.New("unnamed tool in proof")
		}
		full := name
		if namespace != "" {
			full = namespace + "." + name
		}
		if name == "collaboration" || slices.Contains([]string{"spawn_agent", "send_message", "followup_task", "wait_agent", "interrupt_agent", "list_agents"}, name) {
			return nil, errors.New("forbidden capability in proof")
		}
		if item["type"] == "namespace" {
			children, err := inventoryNames(array(item["tools"]), full)
			if err != nil {
				return nil, err
			}
			names = append(names, children...)
		} else {
			names = append(names, full)
		}
	}
	return names, nil
}
