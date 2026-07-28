// Package config defines the small, strict server configuration.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/r3t2/ezgamelan/internal/atomicfile"
)

const FormatVersion = 2

const EnrollPath = "/ezgamelan/v1/enroll"

type VersionError struct {
	Got int
}

func (e *VersionError) Error() string {
	return fmt.Sprintf("unsupported format_version %d, want %d", e.Got, FormatVersion)
}

type Config struct {
	FormatVersion int    `json:"format_version"`
	PublicHost    string `json:"public_host"`
	OpenVPNPort   uint16 `json:"openvpn_port"`
	EnrollURL     string `json:"enroll_url"`
	EnrollListen  string `json:"enroll_listen"`
	VirtualCIDR   string `json:"virtual_cidr"`
	ServerName    string `json:"server_name"`
	MTU           int    `json:"mtu"`
	StateDir      string `json:"state_dir"`
	PKIDir        string `json:"pki_dir"`
	OpenVPNPath   string `json:"openvpn_path"`
}

func Default(publicHost string) Config {
	enrollHost := publicHost
	if ip := net.ParseIP(publicHost); ip != nil && ip.To4() == nil {
		enrollHost = "[" + publicHost + "]"
	}
	enrollURL := url.URL{
		Scheme: "https",
		Host:   enrollHost,
		Path:   EnrollPath,
	}
	return Config{
		FormatVersion: FormatVersion,
		PublicHost:    publicHost,
		OpenVPNPort:   21111,
		EnrollURL:     enrollURL.String(),
		EnrollListen:  "127.0.0.1:21112",
		VirtualCIDR:   "10.86.0.0/24",
		ServerName:    "ezgamelan-server",
		MTU:           1400,
		StateDir:      "/var/lib/ezgamelan",
		PKIDir:        "/etc/ezgamelan/pki",
		OpenVPNPath:   "/usr/sbin/openvpn",
	}
}

func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config %q: %w", path, err)
	}

	var header struct {
		FormatVersion int `json:"format_version"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return Config{}, fmt.Errorf("decode config %q: %w", path, err)
	}
	if header.FormatVersion != FormatVersion {
		return Config{}, &VersionError{Got: header.FormatVersion}
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
	return save(path, cfg, false)
}

func Replace(path string, cfg Config) error {
	return save(path, cfg, true)
}

func save(path string, cfg Config, replace bool) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	data = append(data, '\n')
	write := atomicfile.Create
	if replace {
		write = atomicfile.Replace
	}
	if err := write(path, data, 0o644); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	return nil
}

func (c Config) Validate() error {
	if c.FormatVersion != FormatVersion {
		return &VersionError{Got: c.FormatVersion}
	}
	if err := validateHost(c.PublicHost); err != nil {
		return fmt.Errorf("public_host: %w", err)
	}
	if c.OpenVPNPort == 0 {
		return errors.New("openvpn_port must be in range 1..65535")
	}
	if err := validateEnrollURL(c.EnrollURL); err != nil {
		return fmt.Errorf("enroll_url: %w", err)
	}
	listenHost, _, err := net.SplitHostPort(c.EnrollListen)
	if err != nil {
		return fmt.Errorf("enroll_listen: %w", err)
	}
	listenIP := net.ParseIP(listenHost)
	if listenIP == nil || !listenIP.IsLoopback() {
		return errors.New("enroll_listen must use a loopback IP address")
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
	if !filepath.IsAbs(c.StateDir) || !filepath.IsAbs(c.PKIDir) {
		return errors.New("state_dir and pki_dir must be absolute")
	}
	if !filepath.IsAbs(c.OpenVPNPath) {
		return errors.New("openvpn_path must be absolute")
	}
	return nil
}

func (c Config) OpenVPNEndpoint() string {
	return net.JoinHostPort(c.PublicHost, strconv.Itoa(int(c.OpenVPNPort)))
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

func validateEnrollURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return errors.New("must be an HTTPS URL without user information")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != EnrollPath {
		return fmt.Errorf("must use the fixed path %s without query or fragment", EnrollPath)
	}
	if err := validateHost(parsed.Hostname()); err != nil {
		return err
	}
	if port := parsed.Port(); port != "" {
		value, err := strconv.ParseUint(port, 10, 16)
		if err != nil || value == 0 {
			return errors.New("port must be in range 1..65535")
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
