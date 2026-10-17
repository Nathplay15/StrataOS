package api

import (
	"log/slog"
	"net/http"
	"sync"

	"strataos/internal/fileservices"
	"strataos/internal/store"
)

var fileServicesMu sync.Mutex

// applyFileServices writes the FTP/SFTP configuration for the current accounts.
func (s *Server) applyFileServices() error {
	fileServicesMu.Lock()
	defer fileServicesMu.Unlock()
	var c store.FileServices
	var users, sftpOnly []string
	s.store.Read(func(st *store.State) {
		c = st.Files
		for _, u := range st.Users {
			if u.Disabled || u.Source != "" {
				continue
			}
			users = append(users, u.Username)
			if !u.SSH {
				sftpOnly = append(sftpOnly, u.Username)
			}
		}
	})
	cert, key := s.certs.Files()
	err := fileservices.Apply(c, users, sftpOnly, cert, key)
	if err != nil {
		slog.Warn("file services", "err", err)
	}
	return err
}

func (s *Server) fileServiceRoutes() {
	s.admin("GET /api/fileservices", func(r *http.Request) (any, error) {
		var c store.FileServices
		s.store.Read(func(st *store.State) { c = st.Files })
		_ = fileservices.Validate(&c)
		return map[string]any{"config": c, "ftpInstalled": fileservices.FTPInstalled(), "sftpInstalled": fileservices.SFTPInstalled(), "httpsPort": s.certs.HTTPSPort}, nil
	})

	s.admin("PUT /api/fileservices", func(r *http.Request) (any, error) {
		var c store.FileServices
		if err := decode(r, &c); err != nil {
			return nil, err
		}
		if err := fileservices.Validate(&c); err != nil {
			return nil, badRequest(err.Error())
		}
		if c.FTP && !fileservices.FTPInstalled() {
			return nil, badRequest("install the FTP server first")
		}
		if err := s.store.Update(func(st *store.State) error { st.Files = c; return nil }); err != nil {
			return nil, err
		}
		s.audit(r, "fileservices", "Changed file services (WebDAV, FTP, SFTP)")
		if err := s.applyFileServices(); err != nil {
			return nil, &Error{Status: http.StatusUnprocessableEntity, Msg: err.Error()}
		}
		return nil, nil
	})
}
