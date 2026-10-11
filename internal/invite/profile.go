// Package invite defines credential profiles and one-time tokens.
package invite

import (
	"bytes"
	"crypto"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

const (
	FormatVersion = 1
	MaxSize       = 256 << 10
	MaxNameLength = 32
)

type Profile struct {
	FormatVersion int    `json:"format_version"`
	ClientName    string `json:"client_name"`
	Endpoint      string `json:"endpoint"`
	ServerName    string `json:"server_name"`
	CAPEM         string `json:"ca_pem"`
	CertPEM       string `json:"cert_pem"`
	KeyPEM        string `json:"key_pem"`
	TLSCryptV2PEM string `json:"tls_crypt_v2_pem"`
}

func Parse(data []byte, now time.Time) (Profile, error) {
	p, err := Decode(data)
	if err != nil {
		return Profile{}, err
	}
	if err := p.Validate(now); err != nil {
		return Profile{}, err
	}
	return p, nil
}

// Decode checks the JSON format and identity fields. Certificate validity is
// checked by Validate before use, so expired stored profiles can still be listed.
func Decode(data []byte) (Profile, error) {
	if len(data) == 0 || len(data) > MaxSize {
		return Profile{}, fmt.Errorf("profile size must be in range 1..%d bytes", MaxSize)
	}

	var p Profile
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := decodeProfileObject(dec, &p); err != nil {
		return Profile{}, err
	}
	if err := expectEOF(dec); err != nil {
		return Profile{}, err
	}
	if err := p.validateIdentity(); err != nil {
		return Profile{}, err
	}
	return p, nil
}

func Marshal(p Profile, now time.Time) ([]byte, error) {
	if err := p.Validate(now); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode profile: %w", err)
	}
	data = append(data, '\n')
	if len(data) > MaxSize {
		return nil, fmt.Errorf("encoded profile exceeds %d bytes", MaxSize)
	}
	return data, nil
}

func (p Profile) validateIdentity() error {
	if p.FormatVersion != FormatVersion {
		return fmt.Errorf("unsupported format_version %d", p.FormatVersion)
	}
	if !ValidName(p.ClientName) {
		return errors.New("client_name contains unsupported characters")
	}
	if !ValidNameWithLimit(p.ServerName, 64) {
		return errors.New("server_name contains unsupported characters")
	}
	host, portText, err := net.SplitHostPort(p.Endpoint)
	if err != nil || host == "" {
		return errors.New("endpoint must contain a host and port")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return errors.New("endpoint port must be in range 1..65535")
	}
	if len(host) > 253 {
		return errors.New("endpoint host is too long")
	}
	if net.ParseIP(host) == nil {
		for _, r := range host {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-') {
				return errors.New("endpoint host contains unsupported characters")
			}
		}
	}
	return nil
}

func (p Profile) Validate(now time.Time) error {
	if err := p.validateIdentity(); err != nil {
		return err
	}
	for _, material := range []string{p.CAPEM, p.CertPEM, p.KeyPEM, p.TLSCryptV2PEM} {
		if strings.ContainsAny(material, "<>") {
			return errors.New("inline key material contains configuration delimiters")
		}
	}

	ca, err := parseSingleCertificate("ca_pem", p.CAPEM)
	if err != nil {
		return err
	}
	if !ca.IsCA {
		return errors.New("ca_pem is not a CA certificate")
	}
	cert, err := parseSingleCertificate("cert_pem", p.CertPEM)
	if err != nil {
		return err
	}
	if _, err := verifyClientCertificate(cert, ca, now); err != nil {
		return err
	}
	key, err := parsePrivateKey(p.KeyPEM)
	if err != nil {
		return err
	}
	if err := keyMatchesCertificate(key, cert); err != nil {
		return err
	}
	if err := validateTLSCryptV2(p.TLSCryptV2PEM); err != nil {
		return err
	}
	return nil
}

func (p Profile) ValidateNow() error {
	return p.Validate(time.Now())
}

func ValidName(name string) bool {
	return ValidNameWithLimit(name, MaxNameLength)
}

func ValidNameWithLimit(name string, max int) bool {
	if len(name) == 0 || len(name) > max {
		return false
	}
	for _, r := range name {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') &&
			(r < '0' || r > '9') && r != '-' && r != '_' {
			return false
		}
	}
	return true
}

func decodeProfileObject(dec *json.Decoder, p *Profile) error {
	token, err := dec.Token()
	if err != nil {
		return fmt.Errorf("decode profile: %w", err)
	}
	delim, ok := token.(json.Delim)
	if !ok || delim != '{' {
		return errors.New("profile must be a JSON object")
	}

	seen := make(map[string]struct{}, 8)
	for dec.More() {
		token, err := dec.Token()
		if err != nil {
			return fmt.Errorf("decode profile field: %w", err)
		}
		name, ok := token.(string)
		if !ok {
			return errors.New("profile field name must be a string")
		}
		if _, exists := seen[name]; exists {
			return fmt.Errorf("duplicate field %q", name)
		}
		seen[name] = struct{}{}

		switch name {
		case "format_version":
			err = dec.Decode(&p.FormatVersion)
		case "client_name":
			err = dec.Decode(&p.ClientName)
		case "endpoint":
			err = dec.Decode(&p.Endpoint)
		case "server_name":
			err = dec.Decode(&p.ServerName)
		case "ca_pem":
			err = dec.Decode(&p.CAPEM)
		case "cert_pem":
			err = dec.Decode(&p.CertPEM)
		case "key_pem":
			err = dec.Decode(&p.KeyPEM)
		case "tls_crypt_v2_pem":
			err = dec.Decode(&p.TLSCryptV2PEM)
		default:
			return fmt.Errorf("unknown field %q", name)
		}
		if err != nil {
			return fmt.Errorf("decode field %q: %w", name, err)
		}
	}
	if _, err := dec.Token(); err != nil {
		return fmt.Errorf("decode profile end: %w", err)
	}
	return nil
}

func expectEOF(dec *json.Decoder) error {
	var extra any
	err := dec.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("unexpected trailing JSON value")
	}
	return fmt.Errorf("decode trailing data: %w", err)
}

func parseSingleCertificate(field, text string) (*x509.Certificate, error) {
	block, rest := pem.Decode([]byte(text))
	if block == nil || block.Type != "CERTIFICATE" || len(bytes.TrimSpace(rest)) != 0 {
		return nil, fmt.Errorf("%s must contain exactly one CERTIFICATE block", field)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", field, err)
	}
	return cert, nil
}

func parsePrivateKey(text string) (crypto.Signer, error) {
	block, rest := pem.Decode([]byte(text))
	if block == nil || len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("key_pem must contain exactly one private key block")
	}

	var key any
	var err error
	switch block.Type {
	case "PRIVATE KEY":
		key, err = x509.ParsePKCS8PrivateKey(block.Bytes)
	case "EC PRIVATE KEY":
		key, err = x509.ParseECPrivateKey(block.Bytes)
	case "RSA PRIVATE KEY":
		key, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	default:
		return nil, fmt.Errorf("unsupported private key type %q", block.Type)
	}
	if err != nil {
		return nil, fmt.Errorf("parse key_pem: %w", err)
	}
	signer, ok := key.(crypto.Signer)
	if !ok {
		return nil, errors.New("key_pem does not contain a signing key")
	}
	return signer, nil
}

func verifyClientCertificate(cert, ca *x509.Certificate, now time.Time) ([]*x509.Certificate, error) {
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	chains, err := cert.Verify(x509.VerifyOptions{
		Roots:       roots,
		CurrentTime: now,
		KeyUsages:   []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
	if err != nil {
		return nil, fmt.Errorf("verify client certificate: %w", err)
	}
	return chains[0], nil
}

func keyMatchesCertificate(key crypto.Signer, cert *x509.Certificate) error {
	publicDER, err := x509.MarshalPKIXPublicKey(key.Public())
	if err != nil {
		return fmt.Errorf("marshal private key public part: %w", err)
	}
	if !bytes.Equal(publicDER, cert.RawSubjectPublicKeyInfo) {
		return errors.New("certificate and private key do not match")
	}
	return nil
}

func validateTLSCryptV2(text string) error {
	const begin = "-----BEGIN OpenVPN tls-crypt-v2 client key-----"
	const end = "-----END OpenVPN tls-crypt-v2 client key-----"
	trimmed := strings.TrimSpace(text)
	if len(trimmed) > 16<<10 || !strings.HasPrefix(trimmed, begin+"\n") ||
		!strings.HasSuffix(trimmed, "\n"+end) {
		return errors.New("tls_crypt_v2_pem has invalid format")
	}
	payload := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(trimmed, begin), end))
	if _, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(payload), "")); err != nil || payload == "" {
		return errors.New("tls_crypt_v2_pem contains invalid key data")
	}
	return nil
}
