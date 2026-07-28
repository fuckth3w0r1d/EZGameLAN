package pki

import (
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestInitIsIdempotent(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	pkiDir := filepath.Join(root, "pki")
	now := time.Now().UTC().Truncate(time.Second)

	if err := Init(pkiDir, "ezgamelan-server", now); err != nil {
		t.Fatalf("first Init: %v", err)
	}
	caBefore, err := os.ReadFile(filepath.Join(pkiDir, CAKeyFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := Init(pkiDir, "ezgamelan-server", now.Add(time.Hour)); err != nil {
		t.Fatalf("second Init: %v", err)
	}
	caAfter, err := os.ReadFile(filepath.Join(pkiDir, CAKeyFile))
	if err != nil {
		t.Fatal(err)
	}
	if string(caBefore) != string(caAfter) {
		t.Fatal("second Init replaced the CA key")
	}
}

func TestIssueClientAndWriteCRL(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	pkiDir := filepath.Join(root, "pki")
	now := time.Now().UTC().Truncate(time.Second)
	if err := Init(pkiDir, "ezgamelan-server", now); err != nil {
		t.Fatal(err)
	}
	client, err := IssueClient(pkiDir, "alice", now)
	if err != nil {
		t.Fatalf("IssueClient: %v", err)
	}
	if client.SerialHex == "" {
		t.Fatal("IssueClient returned an empty serial")
	}
	if err := WriteCRL(pkiDir, []Revocation{{SerialHex: client.SerialHex, At: now}}, now); err != nil {
		t.Fatalf("WriteCRL: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(pkiDir, CRLFile))
	if err != nil {
		t.Fatal(err)
	}
	block, rest := pem.Decode(data)
	if block == nil || block.Type != "X509 CRL" || len(rest) != 0 {
		t.Fatal("CRL has invalid PEM encoding")
	}
}
