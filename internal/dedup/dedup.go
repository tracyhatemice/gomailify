package dedup

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Tracker keeps track of forwarded email IDs to prevent duplicates.
// IDs are persisted to a file so they survive restarts.
type Tracker struct {
	mu   sync.Mutex
	ids  map[string]struct{}
	file string
}

// NewTracker loads (or creates) a dedup tracker backed by filePath.
func NewTracker(filePath string) (*Tracker, error) {
	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		return nil, fmt.Errorf("create dedup dir: %w", err)
	}

	t := &Tracker{
		ids:  make(map[string]struct{}),
		file: filePath,
	}

	f, err := os.Open(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return t, nil
		}
		return nil, fmt.Errorf("open dedup file: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			t.ids[line] = struct{}{}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read dedup file: %w", err)
	}

	return t, nil
}

// SeenIDs returns a snapshot of all tracked IDs.
func (t *Tracker) SeenIDs() map[string]struct{} {
	t.mu.Lock()
	defer t.mu.Unlock()
	cp := make(map[string]struct{}, len(t.ids))
	for k := range t.ids {
		cp[k] = struct{}{}
	}
	return cp
}

// MarkSeen adds an ID and persists it to disk.
func (t *Tracker) MarkSeen(id string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, exists := t.ids[id]; exists {
		return nil
	}
	t.ids[id] = struct{}{}

	f, err := os.OpenFile(t.file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open dedup file for append: %w", err)
	}
	defer f.Close()

	if _, err := fmt.Fprintln(f, id); err != nil {
		return fmt.Errorf("write dedup id: %w", err)
	}
	return nil
}

// Count returns the number of tracked IDs.
func (t *Tracker) Count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.ids)
}
