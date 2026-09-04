package gather

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// State is the source of truth between rounds. It lives on the sprite disk.
type State struct {
	Round         int             `json:"round"`
	LastSHA       string          `json:"last_sha"`
	Findings      []PostedFinding `json:"findings"`
	RebuttalCount map[string]int  `json:"rebuttal_count"` // thread id -> replies we posted
}

type PostedFinding struct {
	Path      string `json:"path"`
	Line      int    `json:"line"`
	Dimension string `json:"dimension"`
	Claim     string `json:"claim"`
	Round     int    `json:"round"`
	Resolved  bool   `json:"resolved"`
	ThreadID  string `json:"thread_id,omitempty"`
}

func LoadState(dir string) (*State, error) {
	b, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if errors.Is(err, os.ErrNotExist) {
		return &State{RebuttalCount: map[string]int{}}, nil
	}
	if err != nil {
		return nil, err
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	if s.RebuttalCount == nil {
		s.RebuttalCount = map[string]int{}
	}
	return &s, nil
}

func (s *State) Save(dir string) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "state.json"), b, 0o644)
}

// AlreadyPosted reports whether a finding on the same path, line and dimension
// was posted in an earlier round. Used to suppress repeats on delta reviews.
func (s *State) AlreadyPosted(path string, line int, dimension string) bool {
	for _, f := range s.Findings {
		if f.Path == path && f.Dimension == dimension && abs(f.Line-line) <= 2 {
			return true
		}
	}
	return false
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
