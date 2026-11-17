package store

import (
	"slices"
	"time"
	"unicode/utf8"
)

// AssistantChat is one saved conversation of the web assistant
// (api/h_assistant.go). Only the user/assistant messages are kept (the tool
// exchanges stay ephemeral); the oldest conversations of a user are dropped
// when the cap is reached, to keep the state file small.
type AssistantChat struct {
	ID       string         `json:"id"`
	User     string         `json:"user"`
	Title    string         `json:"title"`
	Messages []AssistantMsg `json:"messages"`
	Updated  time.Time      `json:"updated"`
	// NASData: a tool read data of the NAS in this conversation (the
	// assistant then asks before searching the web).
	NASData bool `json:"nasData,omitempty"`
}

// AssistantMsg is one message of a saved conversation.
type AssistantMsg struct {
	Role    string `json:"role"` // user | assistant
	Content string `json:"content"`
}

const (
	// MaxAssistantChats bounds the conversations kept per user.
	MaxAssistantChats = 20
	// MaxAssistantMessages bounds the messages of one conversation.
	MaxAssistantMessages = 60
	// MaxAssistantChars bounds the length of one saved message.
	MaxAssistantChars = 4000
	// MaxAssistantBytes bounds the text kept for one conversation: the
	// oldest messages go first.
	MaxAssistantBytes = 64 << 10
)

// AssistantChatsOf returns the conversations of one user, newest first.
func (st *State) AssistantChatsOf(user string) []*AssistantChat {
	var out []*AssistantChat
	for i := len(st.AssistantChats) - 1; i >= 0; i-- {
		if st.AssistantChats[i].User == user {
			out = append(out, st.AssistantChats[i])
		}
	}
	return out
}

// AssistantChat finds one conversation of a user.
func (st *State) AssistantChat(user, id string) *AssistantChat {
	for _, c := range st.AssistantChats {
		if c.ID == id && c.User == user {
			return c
		}
	}
	return nil
}

// SaveAssistantChat inserts or updates a conversation (it becomes the most
// recent one), then applies the caps: messages and size of the conversation,
// conversations per user.
func (st *State) SaveAssistantChat(c *AssistantChat) {
	c.Updated = time.Now()
	for i := range c.Messages {
		if m := c.Messages[i].Content; len(m) > MaxAssistantChars {
			m = m[:MaxAssistantChars]
			for len(m) > 0 && !utf8.ValidString(m) {
				m = m[:len(m)-1]
			}
			c.Messages[i].Content = m
		}
	}
	if len(c.Messages) > MaxAssistantMessages {
		c.Messages = c.Messages[len(c.Messages)-MaxAssistantMessages:]
	}
	total := 0
	for _, m := range c.Messages {
		total += len(m.Content)
	}
	for len(c.Messages) > 1 && total > MaxAssistantBytes {
		total -= len(c.Messages[0].Content)
		c.Messages = c.Messages[1:]
	}
	c.Messages = slices.Clip(c.Messages)
	kept := st.AssistantChats[:0:0]
	for _, x := range st.AssistantChats {
		if x.ID != c.ID || x.User != c.User {
			kept = append(kept, x)
		}
	}
	kept = append(kept, c)
	// Per-user cap: drop the oldest conversations beyond the last 20.
	seen := map[string]int{}
	var out []*AssistantChat
	for i := len(kept) - 1; i >= 0; i-- {
		u := kept[i].User
		if seen[u]++; seen[u] <= MaxAssistantChats {
			out = append(out, kept[i])
		}
	}
	slices.Reverse(out)
	st.AssistantChats = out
}

// DeleteAssistantChat removes one conversation of a user.
func (st *State) DeleteAssistantChat(user, id string) {
	for i, c := range st.AssistantChats {
		if c.ID == id && c.User == user {
			st.AssistantChats = append(st.AssistantChats[:i], st.AssistantChats[i+1:]...)
			return
		}
	}
}
