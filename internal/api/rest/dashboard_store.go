package rest

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

type dashboardUser struct {
	Username           string `json:"username"`
	Role               string `json:"role"`
	Disabled           bool   `json:"disabled"`
	MustChangePassword bool   `json:"must_change_password"`
	Salt               []byte `json:"-"`
	Hash               []byte `json:"-"`
}

func (u *dashboardUser) setPassword(password string) {
	u.Salt = make([]byte, 32)
	_, _ = rand.Read(u.Salt)
	u.Hash = dashboardPasswordHash(password, u.Salt)
}

type dashboardSettings struct {
	SiteName       string `json:"site_name"`
	SessionHours   int    `json:"session_hours"`
	RefreshSeconds int    `json:"refresh_seconds"`
}

func defaultDashboardSettings() dashboardSettings { return dashboardSettings{"GideonDB", 8, 5} }

// SQLite connections are short-lived. All mutations hold the authenticator's
// mutex and commit a complete snapshot atomically before acknowledging success.
func openDashboardStore(path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	_ = f.Close()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("admin database permissions must be 0600 or stricter")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`PRAGMA busy_timeout=5000;
 CREATE TABLE IF NOT EXISTS users (
 username TEXT PRIMARY KEY, role TEXT NOT NULL CHECK(role IN ('admin','writer','reader')),
 disabled INTEGER NOT NULL, must_change_password INTEGER NOT NULL,
 salt BLOB NOT NULL, password_hash BLOB NOT NULL);
 CREATE TABLE IF NOT EXISTS settings (
 id INTEGER PRIMARY KEY CHECK(id=1), site_name TEXT NOT NULL,
 session_hours INTEGER NOT NULL CHECK(session_hours BETWEEN 1 AND 24),
 refresh_seconds INTEGER NOT NULL CHECK(refresh_seconds BETWEEN 5 AND 300));`)
	if err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}
func (a *dashboardAuthenticator) persist() error {
	if a.path == "" {
		return nil
	}
	db, err := openDashboardStore(a.path)
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("DELETE FROM users"); err != nil {
		return err
	}
	for _, u := range a.users {
		if _, err = tx.Exec("INSERT INTO users VALUES (?,?,?,?,?,?)", u.Username, u.Role, u.Disabled, u.MustChangePassword, u.Salt, u.Hash); err != nil {
			return err
		}
	}
	if _, err = tx.Exec("INSERT OR REPLACE INTO settings VALUES (1,?,?,?)", a.settings.SiteName, a.settings.SessionHours, a.settings.RefreshSeconds); err != nil {
		return err
	}
	return tx.Commit()
}
func loadDashboardAuthenticator(path string) (*dashboardAuthenticator, error) {
	db, err := openDashboardStore(path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	a := &dashboardAuthenticator{path: path, users: map[string]dashboardUser{}, settings: defaultDashboardSettings(), sessions: map[string]dashboardSessionRecord{}, attempts: map[string]dashboardLoginAttempt{}}
	rows, err := db.Query("SELECT username,role,disabled,must_change_password,salt,password_hash FROM users")
	if err != nil {
		return nil, err
	}
	admins := 0
	for rows.Next() {
		var u dashboardUser
		if err = rows.Scan(&u.Username, &u.Role, &u.Disabled, &u.MustChangePassword, &u.Salt, &u.Hash); err != nil {
			rows.Close()
			return nil, err
		}
		if len(u.Salt) < 16 || len(u.Hash) != 32 {
			rows.Close()
			return nil, fmt.Errorf("invalid password verifier")
		}
		a.users[u.Username] = u
		if u.Role == "admin" && !u.Disabled {
			admins++
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if admins == 0 {
		return nil, fmt.Errorf("admin database has no active administrator")
	}
	err = db.QueryRow("SELECT site_name,session_hours,refresh_seconds FROM settings WHERE id=1").Scan(&a.settings.SiteName, &a.settings.SessionHours, &a.settings.RefreshSeconds)
	if err != nil {
		return nil, err
	}
	return a, nil
}

// PrepareDashboardCredentials migrates the old verifier once. SQLite is the only
// active credential store; the original is removed only after a durable commit.
func PrepareDashboardCredentials(path, username, password string, bootstrap bool) error {
	if _, err := os.Stat(path); err == nil {
		if _, err = loadDashboardAuthenticator(path); err != nil {
			return err
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	legacy := filepath.Join(filepath.Dir(path), "dashboard-credentials.json")
	var a *dashboardAuthenticator
	if info, err := os.Stat(legacy); err == nil {
		if info.Mode().Perm()&0077 != 0 {
			return fmt.Errorf("legacy credentials permissions must be 0600 or stricter")
		}
		data, err := os.ReadFile(legacy)
		if err != nil {
			return err
		}
		var saved dashboardCredentials
		if err = json.Unmarshal(data, &saved); err != nil {
			return err
		}
		salt, e1 := hex.DecodeString(saved.Salt)
		hash, e2 := hex.DecodeString(saved.PasswordHash)
		if saved.Username == "" || saved.Algorithm != "pbkdf2-sha256" || saved.Iterations != dashboardPasswordIterations || e1 != nil || e2 != nil || len(salt) < 16 || len(hash) != 32 {
			return fmt.Errorf("invalid legacy credentials")
		}
		a = newDashboardAuthenticator(saved.Username, "", path, saved.MustChangePassword)
		u := a.users[saved.Username]
		u.Salt, u.Hash = salt, hash
		a.users[saved.Username] = u
	} else if !os.IsNotExist(err) {
		return err
	} else {
		if username == "" || password == "" {
			return fmt.Errorf("dashboard credentials are required")
		}
		a = newDashboardAuthenticator(username, password, path, bootstrap)
	}
	// Build a complete database at a temporary path so interrupted initialization
	// never leaves a partial primary database blocking the next startup.
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".admin-migrate-*")
	if err != nil {
		return err
	}
	tmp := temp.Name()
	temp.Close()
	defer os.Remove(tmp)
	a.path = tmp
	if err = a.persist(); err != nil {
		return err
	}
	if err = os.Rename(tmp, path); err != nil {
		return err
	}
	if err = os.Remove(legacy); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
func (a *dashboardAuthenticator) revokeUserSessions(username string) {
	for token, session := range a.sessions {
		if session.username == username {
			delete(a.sessions, token)
		}
	}
}
