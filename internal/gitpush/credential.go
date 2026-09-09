package gitpush

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// Credential carries in-memory GitHub authentication for one exact destination.
// It is not authority to push and is never part of the plan or journal.
type Credential struct{ destination, header string }

// NewCredential binds a token to one public-GitHub HTTPS repository destination.
// The value is supplied only to Git's child environment, never command arguments.
func NewCredential(destination, token string) (*Credential, error) {
	path, ok := strings.CutPrefix(destination, "https://github.com/")
	parts := strings.Split(path, "/")
	if !ok || len(parts) != 2 {
		return nil, errors.New("credential requires exact GitHub HTTPS repository")
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || len(part) > 104 {
			return nil, errors.New("invalid credential destination")
		}
		for _, r := range part {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.') {
				return nil, errors.New("invalid credential destination")
			}
		}
	}
	if token == "" || len(token) > 4096 {
		return nil, errors.New("invalid GitHub credential")
	}
	for _, r := range token {
		if r < 33 || r > 126 {
			return nil, errors.New("invalid GitHub credential")
		}
	}
	return &Credential{destination: destination, header: "Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+token))}, nil
}

// Format prevents ordinary diagnostic formatting from exposing authentication.
func (c *Credential) Format(s fmt.State, verb rune) {
	_, _ = s.Write([]byte("[GitHub credential redacted]"))
}

func (c *Credential) validate(destination string) error {
	if c == nil {
		return nil
	}
	if c.destination != destination || c.header == "" {
		return errors.New("GitHub credential destination mismatch")
	}
	return nil
}

// ValidateDestination checks explicit credential scope before recording intent.
func (c *Credential) ValidateDestination(destination string) error {
	if c == nil {
		return errors.New("explicit GitHub credential required")
	}
	return c.validate(destination)
}

func (c *Credential) environment(environment []string) []string {
	if c == nil {
		return environment
	}
	return append(environment, "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http."+c.destination+".extraHeader", "GIT_CONFIG_VALUE_0="+c.header)
}
