package openvpn

import (
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/r3t2/ezgamelan/internal/config"
	"github.com/r3t2/ezgamelan/internal/invite"
	"github.com/r3t2/ezgamelan/internal/pki"
)

func ServerConfig(cfg config.Config) (string, error) {
	ip, network, err := net.ParseCIDR(cfg.VirtualCIDR)
	if err != nil {
		return "", fmt.Errorf("parse virtual CIDR: %w", err)
	}
	ip4 := ip.To4()
	if ip4 == nil {
		return "", fmt.Errorf("virtual CIDR is not IPv4")
	}
	mask := net.IP(network.Mask).String()
	serverIP := append(net.IP(nil), ip4...)
	serverIP[3]++
	poolStart := append(net.IP(nil), ip4...)
	poolEnd := append(net.IP(nil), ip4...)
	poolStart[3] += 10
	poolEnd[3] += 19

	lines := []string{
		"port " + strconv.Itoa(int(cfg.OpenVPNPort)),
		"proto udp4",
		"dev tap",
		"mode server",
		"tls-server",
		"ifconfig " + serverIP.String() + " " + mask,
		"ifconfig-pool " + poolStart.String() + " " + poolEnd.String() + " " + mask,
		"ifconfig-pool-persist " + quote(filepath.Join(cfg.StateDir, "ipp.txt")),
		"client-to-client",
		"max-clients 10",
		"ca " + quote(filepath.Join(cfg.PKIDir, pki.CACertFile)),
		"cert " + quote(filepath.Join(cfg.PKIDir, pki.ServerCertFile)),
		"key " + quote(filepath.Join(cfg.PKIDir, pki.ServerKeyFile)),
		"crl-verify " + quote(filepath.Join(cfg.PKIDir, pki.CRLFile)),
		"tls-crypt-v2 " + quote(filepath.Join(cfg.PKIDir, "tls-crypt-v2-server.key")),
		"data-ciphers AES-256-GCM:AES-128-GCM:CHACHA20-POLY1305",
		"allow-compression no",
		"keepalive 10 60",
		"tun-mtu " + strconv.Itoa(cfg.MTU),
		"persist-key",
		"persist-tun",
		"verb 3",
	}
	return strings.Join(lines, "\n") + "\n", nil
}

func ClientConfig(profile invite.Profile, mtu int) (string, error) {
	if err := profile.ValidateNow(); err != nil {
		return "", err
	}
	host, port, err := net.SplitHostPort(profile.Endpoint)
	if err != nil {
		return "", fmt.Errorf("parse endpoint: %w", err)
	}
	if mtu < 1200 || mtu > 1500 {
		return "", fmt.Errorf("MTU must be in range 1200..1500")
	}

	lines := []string{
		"client",
		"dev tap",
		"windows-driver tap-windows6",
		"proto udp4",
		"remote " + quote(host) + " " + port,
		"nobind",
		"persist-key",
		"persist-tun",
		"remote-cert-tls server",
		"verify-x509-name " + quote(profile.ServerName) + " name",
		"pull-filter ignore redirect-gateway",
		"pull-filter ignore dhcp-option",
		"data-ciphers AES-256-GCM:AES-128-GCM:CHACHA20-POLY1305",
		"allow-compression no",
		"auth-nocache",
		"tun-mtu " + strconv.Itoa(mtu),
		"verb 3",
		inline("ca", profile.CAPEM),
		inline("cert", profile.CertPEM),
		inline("key", profile.KeyPEM),
		inline("tls-crypt-v2", profile.TLSCryptV2PEM),
	}
	return strings.Join(lines, "\n") + "\n", nil
}

func quote(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `\"`) + `"`
}

func inline(name, value string) string {
	return "<" + name + ">\n" + strings.TrimSpace(value) + "\n</" + name + ">"
}
