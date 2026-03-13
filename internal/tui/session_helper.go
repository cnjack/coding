package tui

import "github.com/cnjack/coding/internal/session"

// ConvertSessionEntries converts recorded session entries to display-ready
// SessionEntry values for the TUI session-replay view.
func ConvertSessionEntries(entries []session.Entry) []SessionEntry {
	result := make([]SessionEntry, 0, len(entries))
	for _, e := range entries {
		if e.Type == session.EntrySessionStart {
			continue
		}
		result = append(result, SessionEntry{
			Type:    string(e.Type),
			Content: e.Content,
			Name:    e.Name,
			Args:    e.Args,
			Output:  e.Output,
			Error:   e.Error,
		})
	}
	return result
}
