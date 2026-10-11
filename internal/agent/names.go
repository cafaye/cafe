package agent

import (
	"crypto/rand"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// usernameBase turns a display name into the base of a username: lower case,
// alphanumerics and dashes, no leading or trailing dash, bounded. It is the
// same function the legacy CLI uses, because a username the user typed and a
// username cafe generated have to obey the same grammar for
// `cafe agent login --agent <name>` to be able to find either of them.
func usernameBase(name string) string {
	raw := strings.ToLower(strings.TrimSpace(name))
	if raw == "" {
		return "agent"
	}
	var b strings.Builder
	prevDash := false
	for _, r := range raw {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevDash = false
			continue
		}
		if !prevDash {
			b.WriteRune('-')
			prevDash = true
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		slug = "agent"
	}
	if len(slug) > 24 {
		slug = strings.Trim(slug[:24], "-")
		if slug == "" {
			slug = "agent"
		}
	}
	return slug
}

// autoUsername is the username generated when the caller did not name one. The
// suffix is what makes the retry in Store.Register converge: identity refuses a
// taken username with a 409, and the next attempt asks again with a different
// suffix rather than reporting a collision to a person who never chose a name.
func autoUsername(name string) string {
	return usernameBase(name) + "-" + randomSuffix(4)
}

// randomSuffix is 4 characters from a 36-letter alphabet, read from the system
// CSPRNG. The fallback is the clock and it exists because a registration that
// cannot name an agent is worse than one named predictably.
func randomSuffix(length int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	if length <= 0 {
		return ""
	}
	buf := make([]byte, length)
	if _, err := rand.Read(buf); err != nil {
		stamp := fmt.Sprintf("%d", time.Now().UnixNano())
		if len(stamp) < length {
			length = len(stamp)
		}
		return stamp[:length]
	}
	for i := range buf {
		buf[i] = alphabet[int(buf[i])%len(alphabet)]
	}
	return string(buf)
}

// sessionName is the local name one agent-at-one-origin is filed under. The
// host is in it because the same username can be registered against a staging
// identity and a production one, and "the active session" has to mean exactly
// one of those.
func sessionName(username, baseURL string) string {
	return usernameBase(username) + "-" + hostSlug(baseURL)
}

// hostSlug is the origin's host, flattened to the username grammar. An
// unparseable or hostless URL becomes "default" rather than an empty string:
// a session named "noel-" is a session no `--agent` selector can find.
func hostSlug(baseURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || parsed.Hostname() == "" {
		return "default"
	}
	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	if host == "" {
		return "default"
	}
	return usernameBase(host)
}
