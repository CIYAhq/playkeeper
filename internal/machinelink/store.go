package machinelink

import (
	"context"
	"crypto/ed25519"
	"slices"
	"sync"
	"time"
)

// Machine is a machine that joined the dashboard. It maps onto a row of
// the panel's machines table (kind "remote").
type Machine struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// PublicKey is the machine's Ed25519 key: every connection must prove
	// it holds the matching private key.
	PublicKey  ed25519.PublicKey `json:"publicKey"`
	JoinedAt   time.Time         `json:"joinedAt"`
	JoinedFrom string            `json:"joinedFrom,omitempty"`
	// CreatedBy is the account that made the join code it used.
	CreatedBy string `json:"createdBy,omitempty"`
	// Version, LastSeen and LastAddr are updated when it connects and
	// disconnects, and every few minutes while connected.
	Version   string    `json:"version,omitempty"`
	LastSeen  time.Time `json:"lastSeen,omitzero"`
	LastAddr  string    `json:"lastAddr,omitempty"`
	RevokedAt time.Time `json:"revokedAt,omitzero"`
	// RevokedBy is the account that removed it, or machine:<id> when it
	// left by itself.
	RevokedBy string `json:"revokedBy,omitempty"`
}

// Fingerprint is the short form of the machine's key, for people to
// compare with what the machine shows.
func (m Machine) Fingerprint() string { return Fingerprint(m.PublicKey) }

// Removed reports whether the machine was removed or left. Its key never
// works again.
func (m Machine) Removed() bool { return !m.RevokedAt.IsZero() }

// Store keeps join codes and machines; the panel implements it on its
// database. Methods get and return copies.
type Store interface {
	// JoinCodes lists the codes that are waiting, and those used or
	// expired in the last hour.
	JoinCodes(ctx context.Context) ([]JoinCode, error)
	AddJoinCode(ctx context.Context, c JoinCode) error
	// DeleteJoinCode returns ErrNotFound if there is no such code.
	DeleteJoinCode(ctx context.Context, id string) error
	// Pair adds the machine and marks the code used by it (UsedAt =
	// m.JoinedAt, MachineID = m.ID) in one transaction. It returns
	// ErrCodeUsed if the code was used meanwhile and ErrNotFound if it was
	// deleted.
	Pair(ctx context.Context, codeID string, m Machine) error
	// Machines lists machines, removed ones included, oldest first.
	Machines(ctx context.Context) ([]Machine, error)
	Machine(ctx context.Context, id string) (Machine, bool, error)
	// MachineByKey finds the machine with this public key, removed or not.
	MachineByKey(ctx context.Context, key ed25519.PublicKey) (Machine, bool, error)
	// Seen records that the machine was connected at this time, with this
	// Playkeeper version and address.
	Seen(ctx context.Context, id string, at time.Time, version, addr string) error
	// Revoke marks the machine removed. It is not an error to revoke a
	// machine twice; the first time is kept.
	Revoke(ctx context.Context, id string, at time.Time, by string) error
	// JoinFailures are the recent joins refused for their code, oldest
	// first, and SetJoinFailures replaces them, so that a pause after too
	// many of them outlasts a restart.
	JoinFailures(ctx context.Context) ([]JoinFailure, error)
	SetJoinFailures(ctx context.Context, fails []JoinFailure) error
}

// MemoryStore is a Store in memory, for tests and development.
type MemoryStore struct {
	mu       sync.Mutex
	codes    []JoinCode
	machines []Machine
	fails    []JoinFailure
}

// NewMemoryStore returns an empty MemoryStore.
func NewMemoryStore() *MemoryStore { return &MemoryStore{} }

func copyCode(c JoinCode) JoinCode {
	c.Hash = slices.Clone(c.Hash)
	return c
}

func copyMachine(m Machine) Machine {
	m.PublicKey = slices.Clone(m.PublicKey)
	return m
}

func (s *MemoryStore) JoinCodes(context.Context) ([]JoinCode, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]JoinCode, len(s.codes))
	for i, c := range s.codes {
		out[i] = copyCode(c)
	}
	return out, nil
}

func (s *MemoryStore) AddJoinCode(_ context.Context, c JoinCode) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.codes = append(s.codes, copyCode(c))
	return nil
}

func (s *MemoryStore) DeleteJoinCode(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := slices.IndexFunc(s.codes, func(c JoinCode) bool { return c.ID == id })
	if i < 0 {
		return ErrNotFound
	}
	s.codes = slices.Delete(s.codes, i, i+1)
	return nil
}

func (s *MemoryStore) Pair(_ context.Context, codeID string, m Machine) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := slices.IndexFunc(s.codes, func(c JoinCode) bool { return c.ID == codeID })
	if i < 0 {
		return ErrNotFound
	}
	if !s.codes[i].UsedAt.IsZero() {
		return ErrCodeUsed
	}
	s.codes[i].UsedAt = m.JoinedAt
	s.codes[i].MachineID = m.ID
	s.machines = append(s.machines, copyMachine(m))
	return nil
}

func (s *MemoryStore) Machines(context.Context) ([]Machine, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Machine, len(s.machines))
	for i, m := range s.machines {
		out[i] = copyMachine(m)
	}
	return out, nil
}

func (s *MemoryStore) Machine(_ context.Context, id string) (Machine, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.machines {
		if m.ID == id {
			return copyMachine(m), true, nil
		}
	}
	return Machine{}, false, nil
}

func (s *MemoryStore) MachineByKey(_ context.Context, key ed25519.PublicKey) (Machine, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.machines {
		if m.PublicKey.Equal(key) {
			return copyMachine(m), true, nil
		}
	}
	return Machine{}, false, nil
}

func (s *MemoryStore) Seen(_ context.Context, id string, at time.Time, version, addr string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.machines {
		if s.machines[i].ID == id {
			m := &s.machines[i]
			if at.After(m.LastSeen) {
				m.LastSeen = at
			}
			if version != "" {
				m.Version = version
			}
			if addr != "" {
				m.LastAddr = addr
			}
			return nil
		}
	}
	return ErrNotFound
}

func (s *MemoryStore) Revoke(_ context.Context, id string, at time.Time, by string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.machines {
		if s.machines[i].ID == id {
			if s.machines[i].RevokedAt.IsZero() {
				s.machines[i].RevokedAt = at
				s.machines[i].RevokedBy = by
			}
			return nil
		}
	}
	return ErrNotFound
}

func (s *MemoryStore) JoinFailures(context.Context) ([]JoinFailure, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.fails), nil
}

func (s *MemoryStore) SetJoinFailures(_ context.Context, fails []JoinFailure) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fails = slices.Clone(fails)
	return nil
}
