package toolauth

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// InvalidError is a refusal of an auth config's shape. Handlers answer it as a 400 with its message.
type InvalidError struct{ Msg string }

func (e *InvalidError) Error() string { return e.Msg }

func invalid(format string, a ...interface{}) error {
	return &InvalidError{Msg: fmt.Sprintf(format, a...)}
}

// The keys each authentication type may carry. A key outside the list is refused, so a secret cannot be
// kept in a field nothing reads and nothing seals (a "client_secret", a "headers" map): it would sit in the
// tool's row, in exports and in backups, and never be used.
var allowedKeys = map[string][]string{
	"":        {"type", "token", "key", "header", "credential_ref"}, // an older registration with no type: the fields say which
	"bearer":  {"type", "token", "credential_ref"},
	"api-key": {"type", "key", "header", "credential_ref"},
	"api_key": {"type", "key", "header", "credential_ref"},
	"basic":   {"type", "username", "password", "credential_ref"},
}

const maxAuthValue = 4096

// headerNameRE is a header name a person may choose for an api-key: a token, no more than 64 characters.
var headerNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,63}$`)

// forbiddenHeaders are the headers an api-key may not take over: the ones that frame or route a request.
var forbiddenHeaders = map[string]bool{
	"host": true, "content-length": true, "content-type": true, "transfer-encoding": true, "connection": true,
	"upgrade": true, "te": true, "trailer": true, "proxy-authorization": true,
}

// Validate refuses an auth config this edition cannot send: an unsupported type (OAuth client credentials,
// custom header sets, anything else), a key the type does not use, a value that is not text, or a header name
// that is not one. A nil or empty config is no authentication and is fine. A masked value ("****", what
// reading a tool shows) must have been replaced by the stored value before this is called.
func Validate(cfg map[string]interface{}) error {
	if len(cfg) == 0 {
		return nil
	}
	authType, _ := cfg["type"].(string)
	if _, isString := cfg["type"].(string); cfg["type"] != nil && !isString {
		return invalid("auth_config.type must be text")
	}
	allowed, known := allowedKeys[authType]
	if !known {
		return invalid("auth_config.type %q is not supported: use bearer, api-key or basic. OAuth client credentials and custom header sets are not supported, because nothing would send them and the secret would be stored for nothing", authType)
	}
	ok := map[string]bool{}
	for _, k := range allowed {
		ok[k] = true
	}
	var bad []string
	for k, v := range cfg {
		if !ok[k] {
			bad = append(bad, k)
			continue
		}
		if v == nil {
			continue
		}
		str, isString := v.(string)
		if !isString {
			return invalid("auth_config.%s must be text", k)
		}
		if len(str) > maxAuthValue {
			return invalid("auth_config.%s is longer than %d characters", k, maxAuthValue)
		}
		if strings.ContainsAny(str, "\r\n\x00") {
			return invalid("auth_config.%s has a line break or control character in it", k)
		}
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		return invalid("auth_config does not take %s for type %q (it takes %s); put a secret in a credential and name it in credential_ref", strings.Join(bad, ", "), authType, strings.Join(sorted(allowed), ", "))
	}
	if h, _ := cfg["header"].(string); h != "" {
		if !headerNameRE.MatchString(h) || forbiddenHeaders[strings.ToLower(h)] {
			return invalid("auth_config.header %q is not a header name an api-key can use", h)
		}
	}
	return nil
}

func sorted(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

// LiteralSecret finds the one literal secret an auth config of a supported shape holds, so it can be moved
// into a credential: the authentication type, the field it is in, and its value. ok is false when the
// config names a credential already, or holds no literal secret of a shape that can be resolved later.
func LiteralSecret(cfg map[string]interface{}) (authType, field, value string, ok bool) {
	if ref, _ := cfg["credential_ref"].(string); ref != "" {
		return "", "", "", false
	}
	t, _ := cfg["type"].(string)
	if v, _ := cfg["token"].(string); v != "" && (t == "" || t == "bearer") {
		return "bearer", "token", v, true
	}
	if v, _ := cfg["key"].(string); v != "" && (t == "" || t == "api-key" || t == "api_key") {
		return "api-key", "key", v, true
	}
	if v, _ := cfg["password"].(string); v != "" && t == "basic" {
		return "basic", "password", v, true
	}
	return "", "", "", false
}

// StrayFields returns what an auth config of an unsupported shape holds besides its type: every field that
// is not a plain name (type, header, username, credential_ref, or one that already ends in _ref) and has a
// value, text as it is and anything else as JSON. These are the fields an older registration may have kept a
// secret in (a client secret, a headers map); the vault migration moves them out of the tool's row.
func StrayFields(cfg map[string]interface{}) map[string]string {
	out := map[string]string{}
	for k, v := range cfg {
		switch {
		case k == "type" || k == "header" || k == "username" || k == "credential_ref" || strings.HasSuffix(k, "_ref"):
			continue
		case v == nil:
			continue
		}
		if s, isString := v.(string); isString {
			if s != "" {
				out[k] = s
			}
			continue
		}
		out[k] = jsonText(v)
	}
	return out
}

func jsonText(v interface{}) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}
