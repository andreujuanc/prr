package useraccess

import (
	"fmt"
	"net/http"
)

// processUser renders a user as a string.
func processUser(u *User) string {
	return fmt.Sprintf("%s <%s>", u.Name, u.Email)
}

// HandleGetUser returns a single user.
func HandleGetUser(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	u := lookupCached(id)
	fmt.Fprintln(w, processUser(u))
}
