package api

import (
	"net/http"
	"slices"
	"time"

	"strataos/internal/accounts"
	"strataos/internal/store"
	"strataos/internal/sys"
)

func runCmd(name string, args ...string) (string, error) { return sys.Run(name, args...) }

type userView struct {
	Username  string    `json:"username"`
	FullName  string    `json:"fullName"`
	Email     string    `json:"email"`
	Role      string    `json:"role"`
	Disabled  bool      `json:"disabled"`
	SSH       bool      `json:"ssh"`
	Docker    bool      `json:"docker"`
	Groups    []string  `json:"groups"`
	CreatedAt time.Time `json:"createdAt"`
	TwoFactor bool      `json:"twoFactor"`
	Passkeys  int       `json:"passkeys"`
	Source    string    `json:"source,omitempty"`
}

func (s *Server) addToDockerGroup(name string) error {
	_, err := sys.Run("usermod", "-a", "-G", "docker", name)
	return err
}

func (s *Server) adminCount(st *store.State) int {
	n := 0
	for _, u := range st.Users {
		if u.Role == "admin" && !u.Disabled {
			n++
		}
	}
	return n
}

func (s *Server) userRoutes() {
	s.admin("GET /api/users", func(r *http.Request) (any, error) {
		var users []store.User
		s.store.Read(func(st *store.State) {
			for _, u := range st.Users {
				users = append(users, *u)
			}
		})
		out := []userView{}
		for _, u := range users {
			groups := accounts.UserGroups(u.Username)
			v := userView{Username: u.Username, FullName: u.FullName, Email: u.Email, Role: u.Role, Disabled: u.Disabled, SSH: u.SSH, CreatedAt: u.CreatedAt, Groups: []string{}, TwoFactor: u.TOTPSecret != "", Passkeys: len(u.Passkeys), Source: u.Source}
			for _, g := range groups {
				if g == "docker" {
					v.Docker = true
					continue
				}
				v.Groups = append(v.Groups, g)
			}
			out = append(out, v)
		}
		return out, nil
	})

	s.admin("POST /api/users", func(r *http.Request) (any, error) {
		var req struct {
			Username string   `json:"username"`
			Password string   `json:"password"`
			FullName string   `json:"fullName"`
			Email    string   `json:"email"`
			Role     string   `json:"role"`
			Groups   []string `json:"groups"`
			SSH      bool     `json:"ssh"`
			Docker   bool     `json:"docker"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if err := accounts.ValidName(req.Username); err != nil {
			return nil, badRequest(err.Error())
		}
		if err := accounts.ValidPassword(req.Password); err != nil {
			return nil, badRequest(err.Error())
		}
		if req.Role != "admin" {
			req.Role = "user"
		}
		var exists bool
		s.store.Read(func(st *store.State) { exists = st.User(req.Username) != nil })
		if exists {
			return nil, badRequest("user already exists")
		}
		if err := accounts.Create(req.Username, req.FullName, req.Password, req.Groups, req.SSH); err != nil {
			return nil, err
		}
		if req.Docker {
			_ = s.addToDockerGroup(req.Username)
		}
		hash, err := hashPassword(req.Password)
		if err != nil {
			return nil, err
		}
		err = s.store.Update(func(st *store.State) error {
			st.Users = append(st.Users, &store.User{Username: req.Username, FullName: req.FullName, Email: req.Email, Role: req.Role, PasswordHash: hash, SSH: req.SSH, CreatedAt: time.Now()})
			return nil
		})
		s.audit(r, "users", "Created user "+req.Username)
		go s.applyFileServices()
		return nil, err
	})

	s.admin("PUT /api/users/{name}", func(r *http.Request) (any, error) {
		name := r.PathValue("name")
		var req struct {
			FullName string   `json:"fullName"`
			Email    string   `json:"email"`
			Role     string   `json:"role"`
			Disabled bool     `json:"disabled"`
			Groups   []string `json:"groups"`
			SSH      bool     `json:"ssh"`
			Docker   bool     `json:"docker"`
			Password string   `json:"password"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if req.Role != "admin" {
			req.Role = "user"
		}
		var prev store.User
		err := s.store.Update(func(st *store.State) error {
			u := st.User(name)
			if u == nil {
				return notFound("user not found")
			}
			prev = *u
			wasAdmin := u.Role == "admin" && !u.Disabled
			willAdmin := req.Role == "admin" && !req.Disabled
			if wasAdmin && !willAdmin && s.adminCount(st) <= 1 {
				return badRequest("at least one active administrator is required")
			}
			if u.Source != "" {
				// Directory account: the Linux identity and password belong to the directory.
				req.SSH, req.Password = false, ""
			}
			u.FullName, u.Email, u.Role, u.Disabled, u.SSH = req.FullName, req.Email, req.Role, req.Disabled, req.SSH
			return nil
		})
		if err != nil {
			return nil, err
		}
		if prev.Disabled != req.Disabled {
			// The VPN devices of the account follow it.
			go s.vpnUserChanged(name)
		}
		if prev.Source != "" {
			if prev.Role != req.Role || req.Disabled {
				s.sessions.dropUser(name)
			}
			s.audit(r, "users", "Updated directory user "+name)
			return nil, nil
		}
		if err := accounts.Update(name, req.FullName, req.SSH); err != nil {
			return nil, err
		}
		groups := slices.DeleteFunc(slices.Clone(req.Groups), func(g string) bool { return g == "docker" })
		if req.Docker {
			groups = append(groups, "docker")
		}
		if err := accounts.SetUserGroups(name, groups); err != nil {
			return nil, badRequest(err.Error())
		}
		if prev.Disabled != req.Disabled {
			if err := accounts.SetEnabled(name, !req.Disabled); err != nil {
				return nil, err
			}
		}
		if req.Password != "" {
			if err := s.changePassword(name, req.Password); err != nil {
				return nil, err
			}
		}
		if prev.Role != req.Role || req.Disabled || req.Password != "" {
			s.sessions.dropUser(name)
		}
		s.applyShares()
		s.audit(r, "users", "Updated user "+name)
		go s.applyFileServices()
		return nil, nil
	})

	s.admin("DELETE /api/users/{name}", func(r *http.Request) (any, error) {
		name := r.PathValue("name")
		if name == userOf(r) {
			return nil, badRequest("you cannot delete your own account")
		}
		var source string
		err := s.store.Update(func(st *store.State) error {
			u := st.User(name)
			if u == nil {
				return notFound("user not found")
			}
			if u.Role == "admin" && s.adminCount(st) <= 1 {
				return badRequest("at least one administrator is required")
			}
			source = u.Source
			st.Users = slices.DeleteFunc(st.Users, func(x *store.User) bool { return x.Username == name })
			if source == "" {
				for _, sh := range st.Shares {
					delete(sh.Permissions, name)
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		s.sessions.dropUser(name)
		go s.vpnUserGone(name)
		if source != "" {
			// Only the StrataOS profile goes away; the directory account stays.
			s.audit(r, "users", "Removed directory user "+name)
			return nil, nil
		}
		if err := accounts.Delete(name, r.URL.Query().Get("removeHome") == "1"); err != nil {
			return nil, err
		}
		s.applyShares()
		s.audit(r, "users", "Deleted user "+name)
		go s.applyFileServices()
		return nil, nil
	})

	// ---- Groups ----
	s.admin("GET /api/groups", func(r *http.Request) (any, error) { return accounts.ListGroups() })
	s.admin("POST /api/groups", func(r *http.Request) (any, error) {
		var req struct {
			Name    string   `json:"name"`
			Members []string `json:"members"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if err := accounts.CreateGroup(req.Name); err != nil {
			return nil, badRequest(err.Error())
		}
		if len(req.Members) > 0 {
			if err := accounts.SetGroupMembers(req.Name, req.Members); err != nil {
				return nil, err
			}
		}
		s.audit(r, "users", "Created group "+req.Name)
		return nil, nil
	})
	s.admin("PUT /api/groups/{name}", func(r *http.Request) (any, error) {
		var req struct {
			Members []string `json:"members"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if err := accounts.SetGroupMembers(r.PathValue("name"), req.Members); err != nil {
			return nil, err
		}
		s.applyShares()
		return nil, nil
	})
	s.admin("DELETE /api/groups/{name}", func(r *http.Request) (any, error) {
		name := r.PathValue("name")
		if err := accounts.DeleteGroup(name); err != nil {
			return nil, err
		}
		_ = s.store.Update(func(st *store.State) error {
			for _, sh := range st.Shares {
				delete(sh.Permissions, "@"+name)
			}
			return nil
		})
		s.applyShares()
		s.audit(r, "users", "Deleted group "+name)
		return nil, nil
	})
}
