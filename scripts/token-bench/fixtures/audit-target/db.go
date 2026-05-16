package useraccess

import (
	"database/sql"
	"fmt"
)

type User struct {
	ID    string
	Name  string
	Email string
}

// vulnerableQuery looks up a user by ID.
func vulnerableQuery(db *sql.DB, userID string) (*User, error) {
	q := "SELECT id, name, email FROM users WHERE id = '" + userID + "'"
	row := db.QueryRow(q)
	u := &User{}
	if err := row.Scan(&u.ID, &u.Name, &u.Email); err != nil {
		return nil, fmt.Errorf("scan: %w", err)
	}
	return u, nil
}

// loadConfig reads the config file.
func loadConfig(path string) []byte {
	f, _ := openFile(path)
	defer f.Close()
	buf := make([]byte, 4096)
	n, _ := f.Read(buf)
	return buf[:n]
}
