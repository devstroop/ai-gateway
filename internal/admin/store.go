package admin

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

var dummyPasswordHash []byte

func init() {
	hash, err := bcrypt.GenerateFromPassword([]byte("dummy"), bcrypt.DefaultCost)
	if err != nil {
		panic(err)
	}
	dummyPasswordHash = hash
}

type User struct {
	ID           string
	Username     string
	PasswordHash string
	Role         string
}

type Session struct {
	ID        string
	Username  string
	CSRFToken string
	ExpiresAt time.Time
}

var ErrSetupComplete = errors.New("setup already completed")
type Store struct {
	mu       sync.RWMutex
	setupMu  sync.Mutex
	users    map[string]*User
	sessions map[string]*Session
}

func NewStore() *Store {
	return &Store{users: map[string]*User{}, sessions: map[string]*Session{}}
}

func (s *Store) Authenticate(username, password string) (User, bool) {
	s.mu.RLock()
	u, ok := s.users[username]
	var hash string
	if ok {
		hash = u.PasswordHash
	}
	s.mu.RUnlock()
	if !ok {
		_ = bcrypt.CompareHashAndPassword(dummyPasswordHash, []byte(password))
		return User{}, false
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil {
		return User{}, false
	}
	// Revalidate that the password hash has not changed concurrently.
	s.mu.RLock()
	defer s.mu.RUnlock()
	u2, ok := s.users[username]
	if !ok || u2.PasswordHash != hash {
		return User{}, false
	}
	safe := *u2
	safe.PasswordHash = ""
	return safe, true
}

func (s *Store) GetUser(username string) (User, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.users[username]
	if !ok {
		return User{}, false
	}
	safe := *u
	safe.PasswordHash = ""
	return safe, true
}

func (s *Store) CreateSession(username string) (Session, error) {
	s.mu.RLock()
	if _, ok := s.users[username]; !ok {
		s.mu.RUnlock()
		return Session{}, errors.New("user not found")
	}
	s.mu.RUnlock()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return Session{}, err
	}
	id := hex.EncodeToString(b)
	b2 := make([]byte, 32)
	if _, err := rand.Read(b2); err != nil {
		return Session{}, err
	}
	csrf := hex.EncodeToString(b2)
	sess := Session{ID: id, Username: username, CSRFToken: csrf, ExpiresAt: time.Now().Add(12 * time.Hour)}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Re-check user existence and sweep expired sessions.
	if _, ok := s.users[username]; !ok {
		return Session{}, errors.New("user not found")
	}
	now := time.Now()
	for sid, ss := range s.sessions {
		if now.After(ss.ExpiresAt) {
			delete(s.sessions, sid)
		}
	}
	s.sessions[id] = &sess
	return sess, nil
}

func (s *Store) GetSession(id string) (Session, bool) {
	s.mu.RLock()
	sess, ok := s.sessions[id]
	if !ok {
		s.mu.RUnlock()
		return Session{}, false
	}
	if time.Now().After(sess.ExpiresAt) {
		s.mu.RUnlock()
		s.mu.Lock()
		if cur, ok := s.sessions[id]; ok && time.Now().After(cur.ExpiresAt) {
			delete(s.sessions, id)
		}
		s.mu.Unlock()
		return Session{}, false
	}
	out := *sess
	s.mu.RUnlock()
	return out, true
}

func (s *Store) DeleteSession(id string) {
	s.mu.Lock()
	delete(s.sessions, id)
	s.mu.Unlock()
}

func (s *Store) ListUsers() []User {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]User, 0, len(s.users))
	for _, u := range s.users {
		safe := *u
		safe.PasswordHash = ""
		out = append(out, safe)
	}
	return out
}

func (s *Store) CreateUser(username, password, role string) error {
	if username == "" || password == "" {
		return errors.New("username and password required")
	}
	if role == "" {
		role = "user"
	}
	if role != "admin" && role != "user" {
		return errors.New("invalid role")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.users[username]; ok {
		return errors.New("user already exists")
	}
	s.users[username] = &User{ID: username, Username: username, PasswordHash: string(hash), Role: role}
	return nil
}

func (s *Store) ChangePassword(username, newPassword string) error {
	if newPassword == "" {
		return errors.New("password required")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users[username]
	if !ok {
		return errors.New("user not found")
	}
	u.PasswordHash = string(hash)
	// Revoke all sessions for this user.
	for id, sess := range s.sessions {
		if sess.Username == username {
			delete(s.sessions, id)
		}
	}
	return nil
}

func (s *Store) SetupAdmin(username, password string) error {
	if username == "" || password == "" {
		return errors.New("username and password required")
	}
	s.mu.RLock()
	if len(s.users) > 0 {
		s.mu.RUnlock()
		return ErrSetupComplete
	}
	if _, ok := s.users[username]; ok {
		s.mu.RUnlock()
		return errors.New("user already exists")
	}
	s.mu.RUnlock()

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.users) > 0 {
		return ErrSetupComplete
	}
	if _, ok := s.users[username]; ok {
		return errors.New("user already exists")
	}
	s.users[username] = &User{ID: username, Username: username, PasswordHash: string(hash), Role: "admin"}
	return nil
}

func (s *Store) SetupAdminSession(username, password string) (Session, error) {
	if username == "" || password == "" {
		return Session{}, errors.New("username and password required")
	}
	// Serialize setup attempts so concurrent requests don't all pay bcrypt cost.
	s.setupMu.Lock()
	defer s.setupMu.Unlock()
	// Check completion before expensive hashing to avoid CPU DoS on an
	// unauthenticated endpoint.
	s.mu.RLock()
	if len(s.users) > 0 {
		s.mu.RUnlock()
		return Session{}, ErrSetupComplete
	}
	if _, ok := s.users[username]; ok {
		s.mu.RUnlock()
		return Session{}, errors.New("user already exists")
	}
	s.mu.RUnlock()

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return Session{}, err
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return Session{}, err
	}
	id := hex.EncodeToString(b)
	b2 := make([]byte, 32)
	if _, err := rand.Read(b2); err != nil {
		return Session{}, err
	}
	csrf := hex.EncodeToString(b2)
	sess := Session{ID: id, Username: username, CSRFToken: csrf, ExpiresAt: time.Now().Add(12 * time.Hour)}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.users) > 0 {
		return Session{}, ErrSetupComplete
	}
	if _, ok := s.users[username]; ok {
		return Session{}, errors.New("user already exists")
	}
	s.users[username] = &User{ID: username, Username: username, PasswordHash: string(hash), Role: "admin"}
	s.sessions[id] = &sess
	return sess, nil
}

func (s *Store) SetupComplete() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.users) > 0
}

func (s *Store) AuthenticateAndCreateSession(username, password string) (Session, User, error) {
	s.mu.RLock()
	u, ok := s.users[username]
	var hash string
	if ok {
		hash = u.PasswordHash
	}
	s.mu.RUnlock()
	if !ok {
		_ = bcrypt.CompareHashAndPassword(dummyPasswordHash, []byte(password))
		return Session{}, User{}, errors.New("invalid credentials")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil {
		return Session{}, User{}, errors.New("invalid credentials")
	}
	// Generate tokens before acquiring the lock to reduce contention.
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return Session{}, User{}, err
	}
	id := hex.EncodeToString(b)
	b2 := make([]byte, 32)
	if _, err := rand.Read(b2); err != nil {
		return Session{}, User{}, err
	}
	csrf := hex.EncodeToString(b2)

	s.mu.Lock()
	defer s.mu.Unlock()
	u2, ok := s.users[username]
	if !ok || u2.PasswordHash != hash {
		return Session{}, User{}, errors.New("invalid credentials")
	}
	now := time.Now()
	for sid, ss := range s.sessions {
		if now.After(ss.ExpiresAt) {
			delete(s.sessions, sid)
		}
	}
	sess := Session{ID: id, Username: username, CSRFToken: csrf, ExpiresAt: time.Now().Add(12 * time.Hour)}
	s.sessions[id] = &sess
	safe := *u2
	safe.PasswordHash = ""
	return sess, safe, nil
}
