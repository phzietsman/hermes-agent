// Package prompttext holds the review prompts, generated verbatim from the Python source
// (legacy/python/scripts/port/gen_review_prompts.py) so the text the model sees is the text that was tuned.
package prompttext

import (
	_ "embed"
	"encoding/json"
)

//go:embed prompts.json
var raw []byte

// Prompts are the three review prompts.
type Prompts struct {
	Memory   string `json:"memory"`
	Skills   string `json:"skills"`
	Combined string `json:"combined"`
}

// P is loaded once.
var P = func() Prompts {
	var p Prompts
	if err := json.Unmarshal(raw, &p); err != nil {
		panic("learning/prompttext: prompts.json is corrupt: " + err.Error())
	}
	return p
}()
