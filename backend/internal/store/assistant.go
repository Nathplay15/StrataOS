package store

import "time"

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

// SaveAssistantChat inserts or updates a conversation, then applies the caps.
func (st *State) SaveAssistantChat(c *AssistantChat) {
	c.Updated = time.Now()
	replaced := false
	for i, x := range st.AssistantChats {
		if x.ID == c.ID && x.User == c.User {
			st.AssistantChats[i] = c
			replaced = true
			break
		}
	}
	if !replaced {
		st.AssistantChats = append(st.AssistantChats, c)
	}
	// Per-user cap: drop the oldest conversations beyond the last 20.
	var kept []*AssistantChat
	seen := map[string]int{}
	for i := len(st.AssistantChats) - 1; i >= 0; i-- {
		u := st.AssistantChats[i].User
		if seen[u]++; seen[u] <= MaxAssistantChats {
			kept = append([]*AssistantChat{st.AssistantChats[i]}, kept...)
		}
	}
	st.AssistantChats = kept
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
