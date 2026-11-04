// Package experiment judges optimizers by the real bill and task outcomes.
//
// Two kinds of evidence are kept apart. Shadow estimates ("estimated") say what
// an optimization would have removed; they cannot see behaviour changes such as
// retries, extra turns or re-reads. Session-level A/B results ("measured")
// compare whole sessions with the optimizer on against whole sessions with it
// in shadow, using provider-reported cost and outcome signals.
package experiment

import (
	"context"
	"database/sql"
	"errors"
	"hash/fnv"
	"math/rand"
	"strconv"
	"sync"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/store"
)

// Arms. "off" means the optimizer ran in shadow for that session.
const (
	ArmOn  = config.StateOn
	ArmOff = "off"
)

// blockSize is the length of one randomised block. Each block holds exactly
// half "on" and half "off", so arms stay balanced within a stratum at every
// multiple of blockSize sessions.
const blockSize = 4

// StratumFunc names the stratum a session belongs to (for example a hashed
// repo and task type). It must return a label, never a path. Empty means one
// global stratum. Trove's gateway cannot see repos, so the default is empty.
type StratumFunc func(sessionKey string) string

// Assigner gives each session a balanced, randomised, recorded arm. It
// implements measure.Assigner. The first answer for a session and optimizer is
// stored and returned forever, so a session is never switched mid-way and its
// cached prefix stays valid (CLAUDE.md rule 5).
type Assigner struct {
	db      *store.DB
	seed    int64
	now     func() time.Time
	stratum StratumFunc

	mu sync.Mutex
}

// NewAssigner builds an Assigner. now and stratum may be nil.
func NewAssigner(db *store.DB, cfg config.Experiment, now func() time.Time, stratum StratumFunc) *Assigner {
	if now == nil {
		now = time.Now
	}
	return &Assigner{db: db, seed: cfg.Seed, now: now, stratum: stratum}
}

// Arm returns "on" or "off". If the database cannot be used it returns "off"
// without recording anything: the safe answer lowers to shadow and Trove keeps
// working (fail open).
func (a *Assigner) Arm(sessionKey, optimizer string) string {
	arm, err := a.assign(context.Background(), sessionKey, optimizer)
	if err != nil {
		return ArmOff
	}
	return arm
}

func (a *Assigner) assign(ctx context.Context, sessionKey, optimizer string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	var arm string
	err := a.db.QueryRowContext(ctx, `SELECT arm FROM experiment_assignments WHERE session_key = ? AND optimizer = ?`,
		sessionKey, optimizer).Scan(&arm)
	if err == nil {
		return arm, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	stratum := ""
	if a.stratum != nil {
		stratum = a.stratum(sessionKey)
	}
	var seq int
	if err := a.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM experiment_assignments WHERE optimizer = ? AND stratum = ?`,
		optimizer, stratum).Scan(&seq); err != nil {
		return "", err
	}
	arm = BlockArm(a.seed, optimizer, stratum, seq)
	if _, err := a.db.ExecContext(ctx, `INSERT OR IGNORE INTO experiment_assignments
		(session_key, optimizer, arm, stratum, seq, assigned_at) VALUES (?,?,?,?,?,?)`,
		sessionKey, optimizer, arm, stratum, seq, a.now().UnixMilli()); err != nil {
		return "", err
	}
	return arm, nil
}

// BlockArm is the arm for the seq-th session (0-based) of an optimizer in a
// stratum. It is a pure function of its inputs: the same seed, optimizer,
// stratum and position always give the same arm.
func BlockArm(seed int64, optimizer, stratum string, seq int) string {
	block, pos := seq/blockSize, seq%blockSize
	h := fnv.New64a()
	_, _ = h.Write([]byte(strconv.FormatInt(seed, 10) + "\x00" + optimizer + "\x00" + stratum + "\x00" + strconv.Itoa(block)))
	r := rand.New(rand.NewSource(int64(h.Sum64()))) // #nosec G404 -- reproducible assignment, not security
	perm := []string{ArmOn, ArmOn, ArmOff, ArmOff}
	r.Shuffle(len(perm), func(i, j int) { perm[i], perm[j] = perm[j], perm[i] })
	return perm[pos]
}
