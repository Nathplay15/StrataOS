package api

import (
	"context"
	"strings"
	"time"

	"strataos/internal/accounts"
	"strataos/internal/directory"
	"strataos/internal/store"
	"strataos/internal/sys"
)

// directoryLogin checks a directory account and returns its StrataOS user,
// created at the first sign-in. Role: from the directory admin group when one
// is configured, otherwise what an administrator set (user by default).
func (s *Server) directoryLogin(ctx context.Context, login, password string) (store.User, bool) {
	var c store.Directory
	s.store.Read(func(st *store.State) { c = st.Directory })
	if c.JoinedAt.IsZero() {
		return store.User{}, false
	}
	name, ok := directory.Canonical(c, login)
	if !ok {
		return store.User{}, false
	}
	if err := directory.Authenticate(ctx, c, name, password); err != nil {
		return store.User{}, false
	}
	// The spelling the system uses (winbind may change the case).
	if line, err := sys.Run("getent", "passwd", name); err == nil {
		if n, _, ok := strings.Cut(strings.TrimSpace(line), ":"); ok && n != "" {
			name = n
		}
	}
	groups := accounts.UserGroups(name)
	var u store.User
	err := s.store.Update(func(st *store.State) error {
		x := st.User(name)
		if x == nil {
			x = &store.User{Username: name, Role: "user", Source: c.Type, CreatedAt: time.Now()}
			st.Users = append(st.Users, x)
		}
		if x.Source == "" {
			// A local account with that name: the directory never signs in as it.
			return errBadLogin
		}
		if c.AdminGroup != "" {
			x.Role = "user"
			if directory.IsAdmin(c, groups) {
				x.Role = "admin"
			}
		}
		u = *x
		return nil
	})
	if err != nil || u.Disabled {
		return store.User{}, false
	}
	return u, true
}
