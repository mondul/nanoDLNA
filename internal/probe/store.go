package probe

import (
	"encoding/json"
	"log/slog"
	"os"
	"sync"
	"time"

	"nanodlna/internal/cache"
)

// record is what was learned about one file.
type record struct {
	Size     int64   `json:"size"`
	ModUnix  int64   `json:"mod"`
	Valid    bool    `json:"valid"`
	Reason   string  `json:"reason,omitempty"`
	Duration float64 `json:"duration,omitempty"` // seconds
	Width    int     `json:"width,omitempty"`
	Height   int     `json:"height,omitempty"`
}

func (r record) verdict(cached bool) Verdict {
	return Verdict{
		Serve:    r.Valid,
		Reason:   r.Reason,
		Duration: time.Duration(r.Duration * float64(time.Second)),
		Width:    r.Width,
		Height:   r.Height,
		Cached:   cached,
	}
}

// Store remembers verdicts between runs.
//
// Entries are keyed by path and stamped with the file's size and modification
// time, so a file that has been rewritten is checked again while one nobody has
// touched keeps its answer.
type Store struct {
	path string

	mu      sync.Mutex
	records map[string]record
	// touched is every key consulted since the last Save, which is how records
	// for media that is gone are recognised and dropped.
	touched map[string]bool
}

// OpenStore loads the store at path.
//
// A missing file is not an error, and an unreadable one is discarded rather
// than being fatal: the worst consequence is doing work again.
func OpenStore(path string, logger *slog.Logger) *Store {
	if logger == nil {
		logger = slog.Default()
	}
	s := &Store{path: path, records: map[string]record{}, touched: map[string]bool{}}

	data, err := os.ReadFile(path)
	if err != nil {
		return s
	}
	if err := json.Unmarshal(data, &s.records); err != nil {
		logger.Warn("discarding an unreadable validation cache", "path", path, "err", err)
		return &Store{path: path, records: map[string]record{}, touched: map[string]bool{}}
	}
	return s
}

// Get returns the stored record for key, but only while the file still has the
// size and modification time it had when the record was written.
func (s *Store) Get(key string, size int64, mod time.Time) (record, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.touched[key] = true

	rec, ok := s.records[key]
	if !ok || rec.Size != size || rec.ModUnix != mod.Unix() {
		return record{}, false
	}
	return rec, true
}

// Put records what was decided about a file.
func (s *Store) Put(key string, size int64, mod time.Time, verdict Verdict) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.touched[key] = true
	s.records[key] = record{
		Size:     size,
		ModUnix:  mod.Unix(),
		Valid:    verdict.Serve,
		Reason:   verdict.Reason,
		Duration: verdict.Duration.Seconds(),
		Width:    verdict.Width,
		Height:   verdict.Height,
	}
}

// Len is the number of records held.
func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.records)
}

// Save writes the store, dropping records for files that were not consulted
// since the last save because they are no longer in the library.
func (s *Store) Save() error {
	s.mu.Lock()
	for key := range s.records {
		if !s.touched[key] {
			delete(s.records, key)
		}
	}
	s.touched = map[string]bool{}

	data, err := json.MarshalIndent(s.records, "", "  ")
	s.mu.Unlock()

	if err != nil {
		return err
	}
	return cache.WriteFileAtomic(s.path, data, 0o644)
}
