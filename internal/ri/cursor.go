package ri

import (
	"errors"
	"harness.local/engorch/internal/canonical"
)

func cursorAfter(token *string, snapshot, query string) (string, error) {
	if token == nil {
		return "", nil
	}
	var cursor struct {
		Snapshot string `json:"snapshot"`
		Query    string `json:"query"`
		After    string `json:"after"`
	}
	if err := canonical.Decode([]byte(*token), &cursor); err != nil {
		return "", err
	}
	if cursor.Snapshot != snapshot || cursor.Query != query || cursor.After == "" || len(cursor.After) > 256 {
		return "", errors.New("input cursor identity mismatch")
	}
	return cursor.After, nil
}
