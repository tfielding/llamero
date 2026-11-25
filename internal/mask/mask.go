// Package mask hides the parts of an identifier that are worth hiding: phone numbers, UUIDs
// and the like, in logs and in /settings.
package mask

// Mask hides all but the last 4 characters of a phone number or UUID.
func Mask(s string) string {
	if len(s) <= 4 {
		return s
	}
	return "…" + s[len(s)-4:]
}
