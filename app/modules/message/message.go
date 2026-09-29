// Package message is a message shown to a person: an error, a warning or a
// piece of information produced by the server.
//
// The same message serves two audiences. The CLI prints Text, English
// written for server administrators. The Web API returns only Key (a full
// key of the Web UI's language files, e.g. "validation.user.emailTaken") and
// Params, from which the Web UI shows its own wording for its users in the
// viewer's language.
//
// A Message is also an error, so a function can return one where something
// went wrong; the caller then shows it like any other message.
package message

import (
	"errors"
	"maps"
)

// Message is a keyed message. Key is the full language-file key of its Web
// UI wording; Params holds the values that wording needs (nil when none);
// Text is the English wording of the CLI. Key and Text are always given
// together, in one New call.
type Message struct {
	Key    string
	Params map[string]any
	Text   string
}

// Error returns Text, so a Message can be returned as an error.
func (m *Message) Error() string { return m.Text }

// New returns a Message without parameters.
func New(key, text string) *Message {
	return &Message{Key: key, Text: text}
}

// With returns a copy of m with the parameter key set to value (m itself is
// left untouched, so a shared message can be given parameters safely).
func (m *Message) With(key string, value any) *Message {
	c := *m
	c.Params = make(map[string]any, len(m.Params)+1)
	maps.Copy(c.Params, m.Params)
	c.Params[key] = value
	return &c
}

// As returns the Message inside err, if any.
func As(err error) (*Message, bool) {
	var m *Message
	if errors.As(err, &m) {
		return m, true
	}
	return nil, false
}
