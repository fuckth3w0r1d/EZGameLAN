// Package config defines the small, strict server configuration.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/r3t2/ezgamelan/internal/atomicfile"
)

const FormatVersion = 1

type Config struct {
	FormatVersion int    `json:"format_version"`
	PublicHost    string `json:"public_host"`
	OpenVPNPort   uint16 `json:"openvpn_port"`
	EnrollPort    uint16 `json:"enroll_port"`
	EnrollListen  string `json:"enroll_listen"`
	VirtualCIDR   string `json:"virtual_cidr"`
	ServerName    string `json:"server_name"`
	MTU           int    `json:"mtu"`
	StateDir      string `json:"state_dir"`
	PKIDir        string `json:"pki_dir"`
	EnrollTLSDir  string `json:"enroll_tls_dir"`
	OpenVPNPath   string `json:"openvpn_path"`
}

func Default(publicHost string) Config {
	return Config{
		FormatVersion: FormatVersion,
		PublicHost:    publicHost,
		OpenVPNPort:   21111,
		EnrollPort:    21112,
		EnrollListen:  ":21112",
		VirtualCIDR:   "10.86.0.0/24",
		ServerName:    "ezgamelan-server",
		MTU:           1400,
		StateDir:      "/var/lib/ezgamelan",
		PKIDir:        "/etc/ezgamelan/pki",
		EnrollTLSDir:  "/etc/ezgamelan/enroll",
		OpenVPNPath:   "/usr/sbin/openvpn",
	}
}

func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config %q: %w", path, err)
	}

	var cfg Config
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode config %q: %w", path, err)
	}
	if err := ensureJSONEOF(dec); err != nil {
		return Config{}, fmt.Errorf("decode config %q: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("validate config %q: %w", path, err)
	}
	return cfg, nil
}

func Save(path string, cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	data = append(data, '\n')
	if err := atomicfile.Create(path, data, 0o644); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	return nil
}

func (c Config) Validate() error {
	if c.FormatVersion != FormatVersion {
		return fmt.Errorf("unsupported format_version %d", c.FormatVersion)
	}
	if err := validateHost(c.PublicHost); err != nil {
		return fmt.Errorf("public_host: %w", err)
	}
	if c.OpenVPNPort == 0 || c.EnrollPort == 0 {
		return errors.New("ports must be in range 1..65535")
	}
	if _, _, err := net.SplitHostPort(c.EnrollListen); err != nil {
		return fmt.Errorf("enroll_listen: %w", err)
	}
	ip, network, err := net.ParseCIDR(c.VirtualCIDR)
	if err != nil || ip.To4() == nil || network.String() != c.VirtualCIDR {
		return fmt.Errorf("virtual_cidr must be a canonical IPv4 CIDR")
	}
	ones, bits := network.Mask.Size()
	if bits != 32 || ones != 24 {
		return errors.New("virtual_cidr must be an IPv4 /24")
	}
	if !validName(c.ServerName) {
		return errors.New("server_name contains unsupported characters")
	}
	if c.MTU < 1200 || c.MTU > 1500 {
		return errors.New("mtu must be in range 1200..1500")
	}
	if !filepath.IsAbs(c.StateDir) || !filepath.IsAbs(c.PKIDir) ||
		!filepath.IsAbs(c.EnrollTLSDir) {
		return errors.New("state_dir, pki_dir and enroll_tls_dir must be absolute")
	}
	if !filepath.IsAbs(c.OpenVPNPath) {
		return errors.New("openvpn_path must be absolute")
	}
	return nil
}

func (c Config) OpenVPNEndpoint() string {
	return net.JoinHostPort(c.PublicHost, strconv.Itoa(int(c.OpenVPNPort)))
}

func (c Config) EnrollEndpoint() string {
	return net.JoinHostPort(c.PublicHost, strconv.Itoa(int(c.EnrollPort)))
}

func validateHost(host string) error {
	if host == "" || len(host) > 253 {
		return errors.New("host is empty or too long")
	}
	if net.ParseIP(host) != nil {
		return nil
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return errors.New("invalid DNS name")
		}
		for _, r := range label {
			if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') &&
				(r < '0' || r > '9') && r != '-' {
				return errors.New("invalid DNS name")
			}
		}
	}
	return nil
}

func validName(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') &&
			(r < '0' || r > '9') && r != '-' && r != '_' {
			return false
		}
	}
	return true
}

func ensureJSONEOF(dec *json.Decoder) error {
	var extra any
	err := dec.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("unexpected trailing JSON value")
	}
	return err
}
