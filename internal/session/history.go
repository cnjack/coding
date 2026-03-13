package session

import (
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

// ReconstructHistory converts a slice of recorded session entries back into
// LLM history messages suitable for resuming a conversation.
// Only user and assistant text messages are included; tool calls are omitted
// because reconstructing matching tool-call IDs is non-trivial.
func ReconstructHistory(entries []Entry) []adk.Message {
	var msgs []adk.Message
	for _, e := range entries {
		switch e.Type {
		case EntryUser:
			msgs = append(msgs, schema.UserMessage(e.Content))
		case EntryAssistant:
			if e.Content != "" {
				msgs = append(msgs, &schema.Message{Role: schema.Assistant, Content: e.Content})
			}
		}
	}
	return msgs
}
