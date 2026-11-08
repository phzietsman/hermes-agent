//go:build live

package sam

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/phzietsman/hermes-agent/internal/config"
)

// Live checks against the SAM sandbox named in .secrets/creds.env. Read-only: whoami, health and the profile.
// Run: go test -tags live ./internal/memory/sam
func liveClient(t *testing.T) *Client {
	root, _ := filepath.Abs("../../..")
	env := config.LoadEnvFile(filepath.Join(root, ".secrets", "creds.env"))
	if env["AGENT_MEMORY_API_KEY"] == "" || env["AGENT_MEMORY_ENDPOINT"] == "" || env["AGENT_MEMORY_ID"] == "" {
		t.Skip("no SAM credentials in .secrets/creds.env")
	}
	return &Client{Endpoint: env["AGENT_MEMORY_ENDPOINT"], Context: env["AGENT_MEMORY_ID"], APIKey: env["AGENT_MEMORY_API_KEY"],
		Timeout: 30 * time.Second, MaxRetries: 2}
}

func TestLiveWhoamiHealthProfile(t *testing.T) {
	c := liveClient(t)
	ctx := context.Background()
	if me, err := c.Whoami(ctx); err != nil || me["principalId"] == nil {
		t.Fatalf("whoami: %v %v", me, err)
	}
	if _, err := c.Health(ctx); err != nil {
		t.Errorf("health: %v", err)
	}
	if _, err := c.Profile(ctx); err != nil {
		t.Errorf("profile: %v", err)
	}
	bad := *c
	bad.APIKey = "wrong"
	if _, err := bad.Whoami(ctx); !IsAuth(err) {
		t.Errorf("a wrong key should be an auth failure: %v", err)
	}
}
