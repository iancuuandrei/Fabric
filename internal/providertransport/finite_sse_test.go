package providertransport

import "harness.local/engorch/internal/providergateway"

func finiteSSETextResponse(adapter string) []byte {
	switch adapter {
	case providergateway.OpenAIChatCompletionsAdapter:
		return validChatSSE()
	case providergateway.AnthropicMessagesAdapter:
		return responsesEvents(
			`{"type":"message_start","message":{"id":"msg_fixture","type":"message","role":"assistant","content":[],"model":"wire-model","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":3,"output_tokens":1,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":2}}`,
			`{"type":"message_stop"}`,
		)
	default:
		item := `{"id":"msg_fixture","type":"message","status":"completed","content":[{"type":"output_text","annotations":[],"logprobs":[],"text":"ok"}],"role":"assistant"}`
		return responsesEvents(
			`{"type":"response.created","response":`+responsesSnapshot("wire-model", "in_progress", `[]`, `null`)+`,"sequence_number":0}`,
			`{"type":"response.in_progress","response":`+responsesSnapshot("wire-model", "in_progress", `[]`, `null`)+`,"sequence_number":1}`,
			`{"type":"response.output_item.added","output_index":0,"item":{"id":"msg_fixture","type":"message","status":"in_progress","content":[],"role":"assistant"},"sequence_number":2}`,
			`{"type":"response.content_part.added","content_index":0,"item_id":"msg_fixture","output_index":0,"part":{"type":"output_text","annotations":[],"logprobs":[],"text":""},"sequence_number":3}`,
			`{"type":"response.output_text.delta","content_index":0,"delta":"ok","item_id":"msg_fixture","logprobs":[],"output_index":0,"sequence_number":4}`,
			`{"type":"response.output_text.done","content_index":0,"item_id":"msg_fixture","logprobs":[],"output_index":0,"sequence_number":5,"text":"ok"}`,
			`{"type":"response.content_part.done","content_index":0,"item_id":"msg_fixture","output_index":0,"part":{"type":"output_text","annotations":[],"logprobs":[],"text":"ok"},"sequence_number":6}`,
			`{"type":"response.output_item.done","output_index":0,"item":`+item+`,"sequence_number":7}`,
			`{"type":"response.completed","response":`+responsesSnapshot("wire-model", "completed", `[`+item+`]`, `{"input_tokens":3,"input_tokens_details":null,"output_tokens":2,"output_tokens_details":null,"total_tokens":5}`)+`,"sequence_number":8}`,
		)
	}
}
