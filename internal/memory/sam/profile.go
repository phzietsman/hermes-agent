package sam

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// ProfileHeading opens the core profile block of the system prompt.
const ProfileHeading = "## About the user / standing instructions"

// FormatProfile renders the profile: instructions first (they are commands), then preferences, then static facts,
// cut at a line boundary so the block never exceeds maxChars. Empty when there is nothing to say.
func FormatProfile(p Profile, maxChars int) string {
	var lines []string
	lines = append(lines, p.Instructions...)
	lines = append(lines, p.Preferences...)
	lines = append(lines, p.Static...)
	size := utf8.RuneCountInString(ProfileHeading)
	var out []string
	for _, l := range lines {
		item := "- " + strings.TrimSpace(l)
		if size+utf8.RuneCountInString(item)+1 > maxChars {
			break
		}
		out = append(out, item)
		size += utf8.RuneCountInString(item) + 1
	}
	if len(out) == 0 {
		return ""
	}
	return ProfileHeading + "\n" + strings.Join(out, "\n")
}

// ProfileCache is the last good core profile on disk, served when Agent Memory is unreachable.
type ProfileCache struct{ Path string }

// Load returns the cached text, or "" when there is none.
func (c ProfileCache) Load() string {
	raw, err := os.ReadFile(c.Path)
	if err != nil {
		return ""
	}
	var d struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &d) != nil {
		return ""
	}
	return d.Text
}

// Save replaces the cache atomically (mode 0600).
func (c ProfileCache) Save(text string) error {
	if err := os.MkdirAll(filepath.Dir(c.Path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(c.Path), ".core-profile-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := json.NewEncoder(tmp).Encode(map[string]string{"text": text}); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), c.Path)
}
