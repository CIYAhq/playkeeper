package panel

import "net/http"

// setHostCookie sets a cookie on the dashboard's own name alone, for every
// path on it, as a __Host- cookie must be: Secure, Path=/ and no Domain.
// Joined machines own names under the dashboard's domain (<machine
// id>.m.<domain>), and browsers take a cookie for the whole domain from any
// of them, but never a __Host- one, so name must start with __Host- for a
// compromised machine not to plant a cookie the dashboard reads. maxAge -1
// deletes it.
func setHostCookie(w http.ResponseWriter, name, value string, maxAge int, sameSite http.SameSite) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", MaxAge: maxAge, Secure: true, HttpOnly: true, SameSite: sameSite})
}
