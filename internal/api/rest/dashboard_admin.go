package rest

import (
	"crypto/hmac"
	"net/http"
	"regexp"
	"sort"
	"strings"
)

var dashboardUsernamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// Administration always requires an authenticated dashboard administrator.
// API principals do not grant access to dashboard accounts or settings.
func (s *Server) dashboardAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.dashboardAuth == nil {
			writeJSON(w, 503, apiError{Code: "unavailable", Message: "dashboard authentication is unavailable"})
			return
		}
		record, ok := s.dashboardAuth.session(r)
		if !ok {
			writeJSON(w, 401, apiError{Code: "unauthorized", Message: "dashboard login required"})
			return
		}
		if record.mustChange || record.role != "admin" {
			writeJSON(w, 403, apiError{Code: "forbidden", Message: "an administrator with a changed password is required"})
			return
		}
		if r.Method != "GET" && !hmac.Equal([]byte(record.csrf), []byte(r.Header.Get("X-GideonDB-CSRF"))) {
			writeJSON(w, 403, apiError{Code: "csrf_rejected", Message: "valid CSRF token required"})
			return
		}
		next(w, r)
	}
}
func (s *Server) dashboardUsers(w http.ResponseWriter, r *http.Request) {
	a := s.dashboardAuth
	a.mu.Lock()
	defer a.mu.Unlock()
	// Revalidate under the mutation lock to serialize authorization with revocation.
	cookie, cookieErr := r.Cookie(dashboardSessionCookie)
	if cookieErr != nil {
		writeJSON(w, 401, apiError{Code: "unauthorized", Message: "dashboard login required"})
		return
	}
	session, active := a.sessions[cookie.Value]
	actor := a.users[session.username]
	if !active || actor.Disabled || actor.Role != "admin" || actor.MustChangePassword {
		writeJSON(w, 403, apiError{Code: "forbidden", Message: "administrator session is no longer active"})
		return
	}

	if r.Method == "GET" {
		users := make([]dashboardUser, 0, len(a.users))
		for _, u := range a.users {
			users = append(users, u)
		}
		sort.Slice(users, func(i, j int) bool { return users[i].Username < users[j].Username })
		writeJSON(w, 200, map[string]any{"users": users})
		return
	}
	name := r.PathValue("username")
	old, exists := a.users[name]
	var input struct {
		Username string `json:"username"`
		Role     string `json:"role"`
		Disabled bool   `json:"disabled"`
		Password string `json:"password"`
	}
	if r.Method != "DELETE" && !decode(w, r, &input) {
		return
	}
	if r.Method == "POST" {
		name = input.Username
		_, exists = a.users[name]
		if exists {
			writeJSON(w, 409, apiError{Code: "user_exists", Message: "username already exists"})
			return
		}
	} else if !exists {
		writeJSON(w, 404, apiError{Code: "not_found", Message: "user not found"})
		return
	}
	if r.Method != "DELETE" {
		if !dashboardUsernamePattern.MatchString(name) || (input.Role != "admin" && input.Role != "writer" && input.Role != "reader") {
			writeJSON(w, 400, apiError{Code: "invalid_user", Message: "use a valid username and admin, writer, or reader role"})
			return
		}
		if (r.Method == "POST" || input.Password != "") && len(input.Password) < 12 {
			writeJSON(w, 400, apiError{Code: "weak_password", Message: "password must contain at least 12 characters"})
			return
		}
	}
	if name == session.username && (r.Method == "DELETE" || input.Disabled || input.Role != "admin") {
		writeJSON(w, 409, apiError{Code: "self_lockout", Message: "you cannot delete, disable, or demote your own account"})
		return
	}
	if exists && old.Role == "admin" && !old.Disabled && (r.Method == "DELETE" || input.Disabled || input.Role != "admin") {
		count := 0
		for _, u := range a.users {
			if u.Role == "admin" && !u.Disabled {
				count++
			}
		}
		if count <= 1 {
			writeJSON(w, 409, apiError{Code: "last_admin", Message: "at least one active administrator is required"})
			return
		}
	}
	u := old
	if r.Method == "DELETE" {
		delete(a.users, name)
	} else {
		u.Username, u.Role, u.Disabled = name, input.Role, input.Disabled
		if input.Password != "" {
			u.setPassword(input.Password)
			u.MustChangePassword = true
		}
		a.users[name] = u
	}
	if err := a.persist(); err != nil {
		if exists {
			a.users[name] = old
		} else {
			delete(a.users, name)
		}
		writeJSON(w, 500, apiError{Code: "persist_failed", Message: "user change could not be saved"})
		return
	}
	if exists {
		a.revokeUserSessions(name)
	}
	if r.Method == "DELETE" {
		w.WriteHeader(204)
		return
	}
	status := 200
	if r.Method == "POST" {
		status = 201
	}
	writeJSON(w, status, u)
}
func (s *Server) dashboardSettingsHandler(w http.ResponseWriter, r *http.Request) {
	a := s.dashboardAuth
	a.mu.Lock()
	defer a.mu.Unlock()
	// Revalidate under the mutation lock to serialize authorization with revocation.
	cookie, cookieErr := r.Cookie(dashboardSessionCookie)
	if cookieErr != nil {
		writeJSON(w, 401, apiError{Code: "unauthorized", Message: "dashboard login required"})
		return
	}
	session, active := a.sessions[cookie.Value]
	actor := a.users[session.username]
	if !active || actor.Disabled || actor.Role != "admin" || actor.MustChangePassword {
		writeJSON(w, 403, apiError{Code: "forbidden", Message: "administrator session is no longer active"})
		return
	}

	if r.Method == "GET" {
		writeJSON(w, 200, a.settings)
		return
	}
	var input dashboardSettings
	if !decode(w, r, &input) {
		return
	}
	input.SiteName = strings.TrimSpace(input.SiteName)
	if input.SiteName == "" || len(input.SiteName) > 80 || input.SessionHours < 1 || input.SessionHours > 24 || input.RefreshSeconds < 5 || input.RefreshSeconds > 300 {
		writeJSON(w, 400, apiError{Code: "invalid_settings", Message: "site name: 1–80 characters; session: 1–24 hours; refresh: 5–300 seconds"})
		return
	}
	old := a.settings
	a.settings = input
	if err := a.persist(); err != nil {
		a.settings = old
		writeJSON(w, 500, apiError{Code: "persist_failed", Message: "settings could not be saved"})
		return
	}
	writeJSON(w, 200, a.settings)
}

// Every signed-in user can read non-secret display preferences.
func (s *Server) dashboardPreferences(w http.ResponseWriter, r *http.Request) {
	if s.dashboardAuth == nil {
		writeJSON(w, 200, defaultDashboardSettings())
		return
	}
	if _, ok := s.dashboardAuth.session(r); !ok {
		writeJSON(w, 401, apiError{Code: "unauthorized", Message: "dashboard login required"})
		return
	}
	s.dashboardAuth.mu.Lock()
	defer s.dashboardAuth.mu.Unlock()
	writeJSON(w, 200, s.dashboardAuth.settings)
}
