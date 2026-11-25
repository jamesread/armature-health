package health

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"unicode/utf8"

	log "github.com/sirupsen/logrus"
)

// Limits on stored issue fields. They fit the SQL schema (VARCHAR(191) IDs and keys,
// VARCHAR(512) title and link, 64KB TEXT detail and attributes), so one oversized value
// shortens that field instead of failing the whole check run. Text that is not valid UTF-8
// has the invalid bytes replaced with U+FFFD.
const (
	// MaxIDBytes bounds check and source IDs; longer ones are rejected.
	MaxIDBytes = 191
	// MaxKeyBytes bounds issue keys. A longer key (or one that is not valid UTF-8) is stored
	// as a prefix plus a hash of the whole key, so it stays stable and distinct across runs.
	MaxKeyBytes = 191
	// MaxTitleRunes bounds issue titles; longer ones are cut and end with "…".
	MaxTitleRunes = 512
	// MaxLinkRunes bounds issue links; a longer link is dropped, since a cut one is broken.
	MaxLinkRunes = 512
	// MaxDetailRunes bounds issue details; longer ones are cut and end with "…".
	MaxDetailRunes = 16000
	// MaxAttributeValueRunes bounds each attribute value; longer ones are cut and end with "…".
	MaxAttributeValueRunes = 1024
	// MaxAttributesBytes bounds an issue's attributes encoded as JSON. Attributes are kept in
	// key order until the next one would not fit; the rest are dropped with a warning.
	MaxAttributesBytes = 60000

	// maxRunErrorRunes bounds the error recorded on a failed check run.
	maxRunErrorRunes = 4096
	// keyHashBytes is how much of the SHA-256 of an oversized key is kept (hex encoded).
	keyHashBytes = 16
)

// boundText makes s valid UTF-8 and at most n runes long.
func boundText(s string, n int) string {
	s = strings.ToValidUTF8(s, "�")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}

// boundLink drops links that are too long to keep whole.
func boundLink(link string) string {
	link = strings.ToValidUTF8(link, "�")
	if utf8.RuneCountInString(link) > MaxLinkRunes {
		return ""
	}
	return link
}

// boundKey keeps a key that is valid UTF-8 and fits MaxKeyBytes; otherwise it returns a
// prefix of the key plus a hash of the whole key.
func boundKey(key string) string {
	if len(key) <= MaxKeyBytes && utf8.ValidString(key) {
		return key
	}
	sum := sha256.Sum256([]byte(key))
	suffix := "#" + hex.EncodeToString(sum[:keyHashBytes])
	return cutBytes(strings.ToValidUTF8(key, "?"), MaxKeyBytes-len(suffix)) + suffix
}

// cutBytes returns the longest prefix of valid UTF-8 s that is at most n bytes, without
// splitting a rune.
func cutBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// boundAttributes copies attributes, bounding each value and the encoded total (see
// MaxAttributesBytes). It returns nil when there are none.
func boundAttributes(attributes map[string]string) map[string]string {
	if len(attributes) == 0 {
		return nil
	}
	out := make(map[string]string, len(attributes))
	size := len("{}")
	for _, k := range sortedKeys(attributes) {
		v := boundText(attributes[k], MaxAttributeValueRunes)
		size += jsonLen(k) + jsonLen(v) + len(":,")
		if size > MaxAttributesBytes {
			log.WithField("attribute", k).Warn("Health: dropped issue attributes over the size limit")
			break
		}
		out[strings.ToValidUTF8(k, "�")] = v
	}
	return out
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// jsonLen is the length of s encoded as a JSON string.
func jsonLen(s string) int {
	b, _ := json.Marshal(s) // marshalling a string cannot fail
	return len(b)
}

// validID reports whether an ID can be stored: non-empty, valid UTF-8 and within MaxIDBytes.
func validID(id string) bool {
	return id != "" && len(id) <= MaxIDBytes && utf8.ValidString(id)
}
