package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestRevocationPublicationFailureSurvivesAnotherRevocation(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for _, name := range []string{"alice", "bob"} {
		if err := s.writeNewClientUnlocked(Client{Name: name, SerialHex: name, Status: StatusActive, CreatedAt: now, NotAfter: now.Add(time.Hour)}, false); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Revoke("alice", now, func([]Revocation) error { return errors.New("disk failure") }); err == nil {
		t.Fatal("publication failure was hidden")
	}
	s, err = Open(s.root)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Revoke("bob", now, func(records []Revocation) error {
		if len(records) != 2 {
			t.Fatalf("lost revocation: %#v", records)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	clients, err := s.List(now)
	if err != nil {
		t.Fatal(err)
	}
	for _, client := range clients {
		if client.Status != StatusRevoked {
			t.Fatal(client)
		}
	}
	called := false
	if err := s.Revoke("alice", now, func(records []Revocation) error { called = true; return nil }); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("repeated revocation did not reconcile CRL")
	}
}

func TestRecoveryRetainsVerifiedExternalRevocations(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	read := func() ([]Revocation, error) { return []Revocation{{SerialHex: "aa", RevokedAt: time.Now()}}, nil }
	if err := s.ReconcileRevocations(read, func([]Revocation) error { return errors.New("write failed") }); err == nil {
		t.Fatal("missing error")
	}
	records, err := s.readRevocationsUnlocked()
	if err != nil || len(records) != 1 {
		t.Fatalf("ledger = %#v, %v", records, err)
	}
}
