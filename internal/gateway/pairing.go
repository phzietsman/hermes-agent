package gateway

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/phzietsman/hermes-agent/internal/filelock"
)

const (
	pairingTTL     = time.Hour
	maxPending     = 3
	codeAlphabet   = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789" // no 0/O/1/I
	codeLength     = 8
	pairingDirName = "pairing"
)

// ErrPairingFull means too many requests are waiting for approval; none is created until one is approved or expires.
var ErrPairingFull = errors.New("too many pending pairing requests")

// Pairing is the store of people who have been approved to talk to the bot, and of requests waiting for approval.
// A stranger who messages the bot privately gets a code; the owner approves it from the CLI
// (`hermes pairing approve <platform> <code>`), so nobody is let in by chat alone.
type Pairing struct {
	Dir string // <home>/pairing
	Now func() time.Time

	mu sync.Mutex
}

// NewPairing returns the store under home.
func NewPairing(home string) *Pairing { return &Pairing{Dir: filepath.Join(home, pairingDirName)} }

type person struct {
	Name       string `json:"name,omitempty"`
	ApprovedAt string `json:"approved_at,omitempty"`
}

type pendingReq struct {
	UserID    string `json:"user_id"`
	Name      string `json:"name,omitempty"`
	CreatedAt string `json:"created_at"`
}

type pairFile struct {
	Approved map[string]person     `json:"approved"`
	Pending  map[string]pendingReq `json:"pending"`
}

func (p *Pairing) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func safeName(platform string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, strings.ToLower(platform))
}

// edit loads the platform's file under the lock, lets fn change it, and saves when fn returns true.
func (p *Pairing) edit(platform string, fn func(f *pairFile) (bool, error)) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := os.MkdirAll(p.Dir, 0o700); err != nil {
		return err
	}
	lf, err := os.OpenFile(filepath.Join(p.Dir, ".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lf.Close()
	unlock, err := filelock.Lock(lf)
	if err != nil {
		return err
	}
	defer unlock()
	path := filepath.Join(p.Dir, safeName(platform)+".json")
	f := &pairFile{Approved: map[string]person{}, Pending: map[string]pendingReq{}}
	if raw, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(raw, f); err != nil {
			return fmt.Errorf("%s is corrupt: %w (nothing was changed)", path, err)
		}
		if f.Approved == nil {
			f.Approved = map[string]person{}
		}
		if f.Pending == nil {
			f.Pending = map[string]pendingReq{}
		}
	}
	// Expired requests drop out whenever the file is touched.
	for code, r := range f.Pending {
		if t, err := time.Parse(time.RFC3339, r.CreatedAt); err != nil || p.now().Sub(t) > pairingTTL {
			delete(f.Pending, code)
		}
	}
	save, err := fn(f)
	if err != nil || !save {
		return err
	}
	b, _ := json.MarshalIndent(f, "", "  ")
	tmp, err := os.CreateTemp(p.Dir, ".pair-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Approved reports whether userID has been approved on platform. A read error means "not approved": failing closed.
func (p *Pairing) Approved(platform, userID string) bool {
	ok := false
	_ = p.edit(platform, func(f *pairFile) (bool, error) { _, ok = f.Approved[userID]; return false, nil })
	return ok
}

func newCode() string {
	var b [codeLength]byte
	_, _ = rand.Read(b[:])
	out := make([]byte, codeLength)
	for i, x := range b {
		out[i] = codeAlphabet[int(x)%len(codeAlphabet)]
	}
	return string(out)
}

// Request returns the pairing code for a stranger, creating one unless they already have a pending request (then the
// same code is returned and fresh is false, so a repeated message does not mint codes). ErrPairingFull when too many
// requests are waiting.
func (p *Pairing) Request(platform, userID, name string) (code string, fresh bool, err error) {
	err = p.edit(platform, func(f *pairFile) (bool, error) {
		for c, r := range f.Pending {
			if r.UserID == userID {
				code = c
				return false, nil
			}
		}
		if len(f.Pending) >= maxPending {
			return false, ErrPairingFull
		}
		code, fresh = newCode(), true
		f.Pending[code] = pendingReq{UserID: userID, Name: name, CreatedAt: p.now().UTC().Format(time.RFC3339)}
		return true, nil
	})
	return code, fresh, err
}

// Approve accepts a pending code and returns who was approved.
func (p *Pairing) Approve(platform, code string) (userID string, err error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	err = p.edit(platform, func(f *pairFile) (bool, error) {
		r, ok := f.Pending[code]
		if !ok {
			return false, fmt.Errorf("no pending pairing request with code %q on %s (it may have expired)", code, platform)
		}
		delete(f.Pending, code)
		f.Approved[r.UserID] = person{Name: r.Name, ApprovedAt: p.now().UTC().Format(time.RFC3339)}
		userID = r.UserID
		return true, nil
	})
	return userID, err
}

// Revoke removes an approved person.
func (p *Pairing) Revoke(platform, userID string) error {
	return p.edit(platform, func(f *pairFile) (bool, error) {
		if _, ok := f.Approved[userID]; !ok {
			return false, fmt.Errorf("%s is not approved on %s", userID, platform)
		}
		delete(f.Approved, userID)
		return true, nil
	})
}

// Pending is one waiting request.
type Pending struct{ Code, UserID, Name string }

// List returns the approved people (id to name) and the waiting requests, both sorted.
func (p *Pairing) List(platform string) (approved map[string]string, pending []Pending, err error) {
	approved = map[string]string{}
	err = p.edit(platform, func(f *pairFile) (bool, error) {
		for id, who := range f.Approved {
			approved[id] = who.Name
		}
		for c, r := range f.Pending {
			pending = append(pending, Pending{c, r.UserID, r.Name})
		}
		return false, nil
	})
	sort.Slice(pending, func(i, j int) bool { return pending[i].Code < pending[j].Code })
	return approved, pending, err
}
