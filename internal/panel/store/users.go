package store

import (
	"database/sql"
	"errors"
	"fmt"
)

// User is a panel login account.
type User struct {
	ID           int64
	Username     string
	PasswordHash string
}

// CreateUser adds a user; the username must be unique.
func (s *Store) CreateUser(username, hash string) error {
	_, err := s.db.Exec(`INSERT INTO users (username, password_hash) VALUES (?, ?)`, username, hash)
	if err != nil {
		if isUniqueViolation(err, "users.username") {
			return errors.New("nama pengguna sudah ada")
		}
		return fmt.Errorf("insert user: %w", err)
	}
	return nil
}

// GetUser returns the user with the given username, or ErrNotFound.
func (s *Store) GetUser(username string) (User, error) {
	var u User
	err := s.db.QueryRow(`SELECT id, username, password_hash FROM users WHERE username = ?`, username).
		Scan(&u.ID, &u.Username, &u.PasswordHash)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("get user: %w", err)
	}
	return u, nil
}

// SetPasswordHash replaces a user's password hash, or returns ErrNotFound.
func (s *Store) SetPasswordHash(username, hash string) error {
	return s.execOne(`UPDATE users SET password_hash = ? WHERE username = ?`, hash, username)
}

// CountUsers returns the number of users.
func (s *Store) CountUsers() (int, error) {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count users: %w", err)
	}
	return n, nil
}
