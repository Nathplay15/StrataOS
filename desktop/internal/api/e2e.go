package api

import (
	"context"
	"net/url"

	"strataos-sync/internal/e2e"
)

type e2eCtx struct{}

// WithE2E marks the requests made with ctx as those of a client encrypting
// the folder with key ID id: the NAS only lets such clients synchronize an
// end-to-end encrypted folder (and write to it).
func WithE2E(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, e2eCtx{}, id)
}

// E2EState tells whether a NAS folder is end-to-end encrypted.
type E2EState struct {
	Encrypted bool        `json:"encrypted"`
	Root      string      `json:"root,omitempty"`   // the encrypted folder
	Header    *e2e.Header `json:"header,omitempty"` // when the folder asked is that folder
}

// E2E returns the encryption state of a folder.
func (c *Client) E2E(ctx context.Context, folder string) (E2EState, error) {
	var v E2EState
	err := c.call(ctx, "GET", "/api/sync/e2e", url.Values{"folder": {folder}}, nil, &v)
	return v, err
}

// CreateE2E makes an empty folder end-to-end encrypted with that header.
func (c *Client) CreateE2E(ctx context.Context, folder string, h e2e.Header) error {
	return c.call(ctx, "POST", "/api/sync/e2e", nil, map[string]any{"folder": folder, "header": h}, nil)
}

// UpdateE2E replaces the header of an encrypted folder (new passphrase, same key).
func (c *Client) UpdateE2E(ctx context.Context, folder string, h e2e.Header) error {
	return c.call(ctx, "PUT", "/api/sync/e2e", nil, map[string]any{"folder": folder, "header": h}, nil)
}
