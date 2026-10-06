package dice

import (
	"context"
	"crypto/rand"
	"database/sql"
)

// NewRoller creates a new dice roller.
func NewRoller() Roller {
	return &roller{}
}

type roller struct{}

func (r *roller) Roll(ctx context.Context, notation string) (*RollResult, error) {
	return nil, nil // not implemented
}

func (r *roller) RollWithSeed(ctx context.Context, notation string, seed []byte) (*RollResult, error) {
	return nil, nil // not implemented
}

// NewLogStore creates a new dice log store.
func NewLogStore(db *sql.DB) LogStore {
	return &logStore{db: db}
}

type logStore struct {
	db *sql.DB
}

func (s *logStore) Save(ctx context.Context, entry *LogEntry) error {
	return nil // not implemented
}

func (s *logStore) Get(ctx context.Context, rollID string) (*LogEntry, error) {
	return nil, nil // not implemented
}

func (s *logStore) List(ctx context.Context, actorID string, limit int) ([]*LogEntry, error) {
	return nil, nil // not implemented
}

func (s *logStore) PurgeOld(ctx context.Context, days int) error {
	return nil // not implemented
}

// GenerateSeed generates a cryptographically secure seed for dice rolls.
func GenerateSeed() ([]byte, error) {
	seed := make([]byte, 32)
	_, err := rand.Read(seed)
	return seed, err
}
