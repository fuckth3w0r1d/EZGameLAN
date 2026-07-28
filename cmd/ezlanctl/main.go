package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/r3t2/ezgamelan/internal/config"
	"github.com/r3t2/ezgamelan/internal/enroll"
	"github.com/r3t2/ezgamelan/internal/ezlan"
	"github.com/r3t2/ezgamelan/internal/pki"
	"github.com/r3t2/ezgamelan/internal/store"
)

const defaultConfigPath = "/etc/ezgamelan/config.json"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "E_INTERNAL_COMMAND: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	global := flag.NewFlagSet("ezlanctl", flag.ContinueOnError)
	global.SetOutput(os.Stderr)
	configPath := global.String("config", defaultConfigPath, "server configuration path")
	if err := global.Parse(args); err != nil {
		return err
	}
	rest := global.Args()
	if len(rest) == 0 {
		printUsage()
		return errors.New("missing command")
	}

	switch rest[0] {
	case "init":
		return runInit(*configPath, rest[1:])
	case "invite":
		return runInvite(*configPath, rest[1:])
	case "serve":
		return runServe(*configPath, rest[1:])
	case "revoke":
		return runRevoke(*configPath, rest[1:])
	case "list":
		return runList(*configPath, rest[1:])
	case "status":
		return runStatus(*configPath, rest[1:])
	case "help", "-h", "--help":
		printUsage()
		return nil
	default:
		printUsage()
		return fmt.Errorf("unknown command %q", rest[0])
	}
}

func runInit(configPath string, args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	host := fs.String("host", "", "public DNS name or IPv4 address")
	openvpnPort := fs.Uint("openvpn-port", 21111, "OpenVPN UDP port")
	enrollPort := fs.Uint("enroll-port", 21112, "public enrollment TCP port")
	enrollListen := fs.String("enroll-listen", ":21112", "enrollment listen address")
	virtualCIDR := fs.String("subnet", "10.86.0.0/24", "virtual IPv4 /24")
	mtu := fs.Int("mtu", 1400, "TAP MTU")
	stateDir := fs.String("state-dir", "/var/lib/ezgamelan", "state directory")
	pkiDir := fs.String("pki-dir", "/etc/ezgamelan/pki", "CA and OpenVPN PKI directory")
	enrollTLSDir := fs.String(
		"enroll-tls-dir",
		"/etc/ezgamelan/enroll",
		"enrollment TLS directory",
	)
	openvpnPath := fs.String("openvpn", "/usr/sbin/openvpn", "OpenVPN executable")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("init does not accept positional arguments")
	}
	if *host == "" {
		return errors.New("init requires --host HOST")
	}
	if *openvpnPort > 65535 || *enrollPort > 65535 {
		return errors.New("ports must be in range 1..65535")
	}

	cfg := config.Default(*host)
	cfg.OpenVPNPort = uint16(*openvpnPort)
	cfg.EnrollPort = uint16(*enrollPort)
	cfg.EnrollListen = *enrollListen
	cfg.VirtualCIDR = *virtualCIDR
	cfg.MTU = *mtu
	cfg.StateDir = *stateDir
	cfg.PKIDir = *pkiDir
	cfg.EnrollTLSDir = *enrollTLSDir
	cfg.OpenVPNPath = *openvpnPath

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := ezlan.Initialize(ctx, configPath, cfg); err != nil {
		return fmt.Errorf("E_INSTALL_INIT: %w", err)
	}
	fmt.Printf("初始化完成\n配置：%s\nOpenVPN 配置：%s\n",
		configPath,
		filepath.Join(filepath.Dir(configPath), "server.conf"),
	)
	return nil
}

func runInvite(configPath string, args []string) error {
	if len(args) == 0 {
		return errors.New("invite requires CLIENT_NAME")
	}
	name := args[0]
	fs := flag.NewFlagSet("invite", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	output := fs.String("file", "", "write an offline .ezlan file")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("invite has unexpected arguments")
	}
	manager, err := ezlan.Open(configPath)
	if err != nil {
		return fmt.Errorf("E_JOIN_CONFIG: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := manager.Invite(ctx, name, *output)
	if err != nil {
		return fmt.Errorf("E_JOIN_CREATE: %w", err)
	}
	if result.File != "" {
		fmt.Printf("离线配置已生成：%s\n请单独发送给 %s，导入后删除源文件。\n", result.File, name)
		return nil
	}
	fmt.Printf("加入链接（%s 前有效）：\n%s\n", result.ExpiresAt.Local().Format(time.RFC3339), result.Link)
	return nil
}

func runServe(configPath string, args []string) error {
	if len(args) != 0 {
		return errors.New("serve does not accept arguments")
	}
	manager, err := ezlan.Open(configPath)
	if err != nil {
		return fmt.Errorf("E_JOIN_CONFIG: %w", err)
	}
	cfg := manager.Config()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	api := enroll.New(manager.State(), logger)
	server := &http.Server{
		Addr:              cfg.EnrollListen,
		Handler:           api.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    8 << 10,
		TLSConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
		},
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	errCh := make(chan error, 1)
	go func() {
		errCh <- server.ListenAndServeTLS(
			filepath.Join(cfg.EnrollTLSDir, pki.EnrollCertFile),
			filepath.Join(cfg.EnrollTLSDir, pki.EnrollKeyFile),
		)
	}()
	logger.Info("enrollment service started", "listen", cfg.EnrollListen)
	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("E_JOIN_SERVE: %w", err)
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("E_JOIN_SHUTDOWN: %w", err)
	}
	err = <-errCh
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("E_JOIN_SERVE: %w", err)
	}
	return nil
}

func runRevoke(configPath string, args []string) error {
	if len(args) != 1 {
		return errors.New("revoke requires exactly one CLIENT_NAME")
	}
	manager, err := ezlan.Open(configPath)
	if err != nil {
		return fmt.Errorf("E_JOIN_CONFIG: %w", err)
	}
	if err := manager.Revoke(args[0]); err != nil {
		return fmt.Errorf("E_JOIN_REVOKE: %w", err)
	}
	fmt.Printf("已吊销：%s\n已有连接将在重新认证时失效；需要立即断开时重启 OpenVPN。\n", args[0])
	return nil
}

func runList(configPath string, args []string) error {
	if len(args) != 0 {
		return errors.New("list does not accept arguments")
	}
	manager, err := ezlan.Open(configPath)
	if err != nil {
		return fmt.Errorf("E_JOIN_CONFIG: %w", err)
	}
	clients, err := manager.List()
	if err != nil {
		return fmt.Errorf("E_JOIN_LIST: %w", err)
	}
	if len(clients) == 0 {
		fmt.Println("暂无客户端")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tSTATUS\tEXPIRES")
	for _, client := range clients {
		fmt.Fprintf(w, "%s\t%s\t%s\n", client.Name, client.Status, client.NotAfter.Local().Format("2006-01-02"))
	}
	return w.Flush()
}

func runStatus(configPath string, args []string) error {
	if len(args) != 0 {
		return errors.New("status does not accept arguments")
	}
	manager, err := ezlan.Open(configPath)
	if err != nil {
		return fmt.Errorf("E_JOIN_CONFIG: %w", err)
	}
	cfg := manager.Config()
	clients, err := manager.List()
	if err != nil {
		return fmt.Errorf("E_JOIN_STATUS: %w", err)
	}
	counts := map[store.ClientStatus]int{}
	for _, client := range clients {
		counts[client.Status]++
	}
	pin, err := pki.EnrollmentPin(cfg.EnrollTLSDir)
	if err != nil {
		return fmt.Errorf("E_JOIN_STATUS: %w", err)
	}
	fmt.Printf(
		"服务器：%s\nOpenVPN：%s\n加入服务：https://%s\n服务指纹：%s\n客户端：active=%s pending=%s revoked=%s\n",
		cfg.PublicHost,
		cfg.OpenVPNEndpoint(),
		cfg.EnrollEndpoint(),
		pin,
		strconv.Itoa(counts[store.StatusActive]),
		strconv.Itoa(counts[store.StatusPending]),
		strconv.Itoa(counts[store.StatusRevoked]),
	)
	return nil
}

func printUsage() {
	fmt.Fprintln(os.Stderr, `EZGameLAN server control

Usage:
  ezlanctl [--config PATH] init --host HOST [options]
  ezlanctl [--config PATH] invite CLIENT_NAME [--file PATH]
  ezlanctl [--config PATH] serve
  ezlanctl [--config PATH] revoke CLIENT_NAME
  ezlanctl [--config PATH] list
  ezlanctl [--config PATH] status`)
}
