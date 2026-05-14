package useraccess

import "os"

// Stringer is implemented by anything that has a String method.
type Stringer interface {
	String() string
}

// userLabel wraps a user so it satisfies Stringer.
type userLabel struct{ u *User }

func (l userLabel) String() string { return l.u.Name }

// label is the single caller of Stringer.
func label(s Stringer) string { return s.String() }

// openFile is a thin wrapper around os.Open.
func openFile(path string) (*os.File, error) {
	return os.Open(path)
}

// unusedHelper is not called from anywhere.
func unusedHelper(n int) int {
	total := 0
	for i := 0; i < n; i++ {
		total += i * 2
	}
	return total
}

// lookupCached returns a fake cached user.
func lookupCached(id string) *User {
	if id == "admin" {
		return &User{ID: id, Name: "Admin", Email: "admin@example.com"}
	}
	return nil
}
