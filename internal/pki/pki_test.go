package pki

import (
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestInitIsIdempotentAndSeparatesEnrollmentKey(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	pkiDir := filepath.Join(root, "pki")
	enrollDir := filepath.Join(root, "enroll")
	now := time.Now().UTC().Truncate(time.Second)

	if err := Init(pkiDir, enrollDir, "127.0.0.1", "ezgamelan-server", now); err != nil {
		t.Fatalf("first Init: %v", err)
	}
	caBefore, err := os.ReadFile(filepath.Join(pkiDir, CAKeyFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := Init(pkiDir, enrollDir, "127.0.0.1", "ezgamelan-server", now.Add(time.Hour)); err != nil {
		t.Fatalf("second Init: %v", err)
	}
	caAfter, err := os.ReadFile(filepath.Join(pkiDir, CAKeyFile))
	if err != nil {
		t.Fatal(err)
	}
	if string(caBefore) != string(caAfter) {
		t.Fatal("second Init replaced the CA key")
	}
	if _, err := os.Stat(filepath.Join(enrollDir, EnrollKeyFile)); err != nil {
		t.Fatalf("enrollment key: %v", err)
	}
	if _, err := os.Stat(filepath.Join(enrollDir, CAKeyFile)); !os.IsNotExist(err) {
		t.Fatalf("CA key appeared in enrollment directory: %v", err)
	}
}

func TestIssueClientAndWriteCRL(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	pkiDir := filepath.Join(root, "pki")
	now := time.Now().UTC().Truncate(time.Second)
	if err := Init(pkiDir, filepath.Join(root, "enroll"), "vpn.test", "ezgamelan-server", now); err != nil {
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

func TestEnrollmentPinIsStable(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	enrollDir := filepath.Join(root, "enroll")
	if err := Init(filepath.Join(root, "pki"), enrollDir, "vpn.test", "ezgamelan-server", time.Now()); err != nil {
		t.Fatal(err)
	}
	first, err := EnrollmentPin(enrollDir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := EnrollmentPin(enrollDir)
	if err != nil {
		t.Fatal(err)
	}
	if first == "" || first != second {
		t.Fatalf("pins = %q, %q", first, second)
	}
}
