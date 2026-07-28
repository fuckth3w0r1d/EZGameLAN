// Package pki manages the small local certificate authority used by a single
// EZGameLAN server.
package pki

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/r3t2/ezgamelan/internal/atomicfile"
)

const (
	CACertFile     = "ca.crt"
	CAKeyFile      = "ca.key"
	ServerCertFile = "server.crt"
	ServerKeyFile  = "server.key"
	EnrollCertFile = "enroll.crt"
	EnrollKeyFile  = "enroll.key"
	CRLFile        = "crl.pem"
)

type ClientMaterial struct {
	CertPEM   []byte
	KeyPEM    []byte
	SerialHex string
	NotAfter  time.Time
}

type Revocation struct {
	SerialHex string
	At        time.Time
}

func Init(dir, enrollDir, publicHost, serverName string, now time.Time) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create PKI directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("secure PKI directory: %w", err)
	}

	if err := os.MkdirAll(enrollDir, 0o700); err != nil {
		return fmt.Errorf("create enrollment TLS directory: %w", err)
	}
	if err := os.Chmod(enrollDir, 0o700); err != nil {
		return fmt.Errorf("secure enrollment TLS directory: %w", err)
	}

	caCertPath := filepath.Join(dir, CACertFile)
	caKeyPath := filepath.Join(dir, CAKeyFile)
	caCertExists, err := regularFileExists(caCertPath)
	if err != nil {
		return err
	}
	caKeyExists, err := regularFileExists(caKeyPath)
	if err != nil {
		return err
	}
	if caCertExists != caKeyExists {
		return errors.New("existing CA is incomplete")
	}
	if !caCertExists {
		caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return fmt.Errorf("generate CA key: %w", err)
		}
		caSerial, err := randomSerial()
		if err != nil {
			return err
		}
		caTemplate := x509.Certificate{
			SerialNumber:          caSerial,
			Subject:               pkix.Name{CommonName: "EZGameLAN Local CA"},
			NotBefore:             now.Add(-5 * time.Minute),
			NotAfter:              now.AddDate(10, 0, 0),
			KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
			BasicConstraintsValid: true,
			IsCA:                  true,
			MaxPathLen:            0,
		}
		caDER, err := x509.CreateCertificate(
			rand.Reader,
			&caTemplate,
			&caTemplate,
			&caKey.PublicKey,
			caKey,
		)
		if err != nil {
			return fmt.Errorf("create CA certificate: %w", err)
		}
		caKeyPEM, err := marshalPrivateKey(caKey)
		if err != nil {
			return err
		}
		if err := atomicfile.Create(caKeyPath, caKeyPEM, 0o600); err != nil {
			return fmt.Errorf("write CA key: %w", err)
		}
		if err := atomicfile.Create(
			caCertPath,
			pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}),
			0o644,
		); err != nil {
			return fmt.Errorf("write CA certificate: %w", err)
		}
	}

	caCert, caKey, err := loadCA(dir)
	if err != nil {
		return err
	}

	serverCertPath := filepath.Join(dir, ServerCertFile)
	serverKeyPath := filepath.Join(dir, ServerKeyFile)
	if err := ensureCertificatePair(
		serverCertPath,
		serverKeyPath,
		func() ([]byte, []byte, error) {
			return issueCertificate(
				caCert,
				caKey,
				serverName,
				[]x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
				now,
				now.AddDate(3, 0, 0),
				nil,
				nil,
			)
		},
	); err != nil {
		return fmt.Errorf("initialize OpenVPN server certificate: %w", err)
	}

	enrollCertPath := filepath.Join(enrollDir, EnrollCertFile)
	enrollKeyPath := filepath.Join(enrollDir, EnrollKeyFile)
	if err := ensureCertificatePair(
		enrollCertPath,
		enrollKeyPath,
		func() ([]byte, []byte, error) {
			return selfSignedEnrollmentCertificate(publicHost, now)
		},
	); err != nil {
		return fmt.Errorf("initialize enrollment TLS certificate: %w", err)
	}

	if exists, err := regularFileExists(filepath.Join(dir, CRLFile)); err != nil {
		return err
	} else if !exists {
		if err := WriteCRL(dir, nil, now); err != nil {
			return err
		}
	}
	return nil
}

func IssueClient(dir, name string, now time.Time) (ClientMaterial, error) {
	caCert, caKey, err := loadCA(dir)
	if err != nil {
		return ClientMaterial{}, err
	}
	certPEM, keyPEM, err := issueCertificate(
		caCert,
		caKey,
		name,
		[]x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		now,
		now.AddDate(3, 0, 0),
		nil,
		nil,
	)
	if err != nil {
		return ClientMaterial{}, fmt.Errorf("issue client certificate: %w", err)
	}
	block, _ := pem.Decode(certPEM)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return ClientMaterial{}, fmt.Errorf("parse issued client certificate: %w", err)
	}
	return ClientMaterial{
		CertPEM:   certPEM,
		KeyPEM:    keyPEM,
		SerialHex: hex.EncodeToString(cert.SerialNumber.Bytes()),
		NotAfter:  cert.NotAfter,
	}, nil
}

func ReadCACertificate(dir string) ([]byte, error) {
	data, err := os.ReadFile(filepath.Join(dir, CACertFile))
	if err != nil {
		return nil, fmt.Errorf("read CA certificate: %w", err)
	}
	return data, nil
}

func EnrollmentPin(enrollDir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(enrollDir, EnrollCertFile))
	if err != nil {
		return "", fmt.Errorf("read enrollment certificate: %w", err)
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return "", errors.New("enrollment certificate has invalid PEM format")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", fmt.Errorf("parse enrollment certificate: %w", err)
	}
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

func WriteCRL(dir string, revoked []Revocation, now time.Time) error {
	caCert, caKey, err := loadCA(dir)
	if err != nil {
		return err
	}
	entries := make([]x509.RevocationListEntry, 0, len(revoked))
	for _, item := range revoked {
		serialBytes, err := hex.DecodeString(item.SerialHex)
		if err != nil || len(serialBytes) == 0 {
			return fmt.Errorf("invalid revoked serial %q", item.SerialHex)
		}
		entries = append(entries, x509.RevocationListEntry{
			SerialNumber:   new(big.Int).SetBytes(serialBytes),
			RevocationTime: item.At,
		})
	}
	number := big.NewInt(now.UnixNano())
	if number.Sign() <= 0 {
		number = big.NewInt(1)
	}
	template := x509.RevocationList{
		Number:                    number,
		ThisUpdate:                now.Add(-time.Minute),
		NextUpdate:                now.AddDate(1, 0, 0),
		RevokedCertificateEntries: entries,
	}
	der, err := x509.CreateRevocationList(rand.Reader, &template, caCert, caKey)
	if err != nil {
		return fmt.Errorf("create CRL: %w", err)
	}
	data := pem.EncodeToMemory(&pem.Block{Type: "X509 CRL", Bytes: der})
	if err := atomicfile.Replace(filepath.Join(dir, CRLFile), data, 0o644); err != nil {
		return fmt.Errorf("write CRL: %w", err)
	}
	return nil
}

func loadCA(dir string) (*x509.Certificate, crypto.Signer, error) {
	certData, err := os.ReadFile(filepath.Join(dir, CACertFile))
	if err != nil {
		return nil, nil, fmt.Errorf("read CA certificate: %w", err)
	}
	certBlock, rest := pem.Decode(certData)
	if certBlock == nil || certBlock.Type != "CERTIFICATE" || len(rest) != 0 {
		return nil, nil, errors.New("CA certificate has invalid PEM format")
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("parse CA certificate: %w", err)
	}
	if !cert.IsCA {
		return nil, nil, errors.New("CA certificate is not a CA")
	}

	keyData, err := os.ReadFile(filepath.Join(dir, CAKeyFile))
	if err != nil {
		return nil, nil, fmt.Errorf("read CA key: %w", err)
	}
	keyBlock, rest := pem.Decode(keyData)
	if keyBlock == nil || keyBlock.Type != "PRIVATE KEY" || len(rest) != 0 {
		return nil, nil, errors.New("CA key has invalid PEM format")
	}
	keyAny, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("parse CA key: %w", err)
	}
	key, ok := keyAny.(crypto.Signer)
	if !ok {
		return nil, nil, errors.New("CA key is not a signing key")
	}
	return cert, key, nil
}

func issueCertificate(
	ca *x509.Certificate,
	caKey crypto.Signer,
	commonName string,
	extendedUsage []x509.ExtKeyUsage,
	notBefore time.Time,
	notAfter time.Time,
	dnsNames []string,
	ipAddresses []net.IP,
) ([]byte, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate private key: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, nil, err
	}
	template := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    notBefore.Add(-5 * time.Minute),
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  extendedUsage,
		DNSNames:     dnsNames,
		IPAddresses:  ipAddresses,
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, ca, &key.PublicKey, caKey)
	if err != nil {
		return nil, nil, fmt.Errorf("create certificate: %w", err)
	}
	keyPEM, err := marshalPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), keyPEM, nil
}

func selfSignedEnrollmentCertificate(publicHost string, now time.Time) ([]byte, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate enrollment TLS key: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, nil, err
	}
	template := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "EZGameLAN Enrollment"},
		NotBefore:    now.Add(-5 * time.Minute),
		NotAfter:     now.AddDate(5, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if ip := net.ParseIP(publicHost); ip != nil {
		template.IPAddresses = []net.IP{ip}
	} else {
		template.DNSNames = []string{publicHost}
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, fmt.Errorf("create enrollment TLS certificate: %w", err)
	}
	keyPEM, err := marshalPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), keyPEM, nil
}

func marshalPrivateKey(key crypto.Signer) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("marshal private key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

func randomSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, fmt.Errorf("generate certificate serial: %w", err)
	}
	if serial.Sign() == 0 {
		serial.SetInt64(1)
	}
	return serial, nil
}

func ensureCertificatePair(
	certPath, keyPath string,
	generate func() ([]byte, []byte, error),
) error {
	certExists, err := regularFileExists(certPath)
	if err != nil {
		return err
	}
	keyExists, err := regularFileExists(keyPath)
	if err != nil {
		return err
	}
	if certExists != keyExists {
		return errors.New("certificate pair is incomplete")
	}
	if certExists {
		return nil
	}
	cert, key, err := generate()
	if err != nil {
		return err
	}
	if err := atomicfile.Create(keyPath, key, 0o600); err != nil {
		return err
	}
	if err := atomicfile.Create(certPath, cert, 0o644); err != nil {
		return err
	}
	return nil
}

func regularFileExists(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("%q is not a regular file", path)
	}
	return true, nil
}
