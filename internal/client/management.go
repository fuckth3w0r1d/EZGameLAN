package client

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/r3t2/ezgamelan/internal/ipc"
	"github.com/r3t2/ezgamelan/internal/join"
	"github.com/r3t2/ezgamelan/internal/netcfg"
)

type managementSession struct{ address, password, passwordFile, adapterGUID string }

func (m *Manager) prepareManagement(configPath string) (*managementSession, error) {
	if m.options.EnsureAdapter == nil {
		return nil, errors.New("E_ADAPTER: 专用 TAP 网卡未配置")
	}
	ctx, cancel := context.WithTimeout(m.ctx, 15*time.Second)
	defer cancel()
	guid, err := m.options.EnsureAdapter(ctx)
	if err != nil {
		if errorCode(err) == "E_ADAPTER_REBOOT" {
			return nil, err
		}
		return nil, fmt.Errorf("E_ADAPTER: %w", err)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	address := listener.Addr().String()
	listener.Close()
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	password := hex.EncodeToString(secret)
	file, err := os.CreateTemp(filepath.Dir(configPath), ".management-*")
	if err != nil {
		return nil, err
	}
	path := file.Name()
	if _, err := file.WriteString(password + "\n"); err != nil {
		file.Close()
		os.Remove(path)
		return nil, err
	}
	if err := file.Close(); err != nil {
		os.Remove(path)
		return nil, err
	}
	if err := join.ProtectPath(ctx, path, false, ""); err != nil {
		os.Remove(path)
		return nil, err
	}
	return &managementSession{address: address, password: password, passwordFile: path, adapterGUID: guid}, nil
}

func (s *managementSession) close() { _ = os.Remove(s.passwordFile) }
func (s *managementSession) arguments() []string {
	host, port, _ := net.SplitHostPort(s.address)
	return []string{"--dev-node", s.adapterGUID, "--management", host, port, s.passwordFile, "--management-hold"}
}

func (m *Manager) monitorManagement(session *managementSession, generation uint64, done <-chan struct{}) {
	defer session.close()
	ctx, cancel := context.WithCancel(m.ctx)
	defer cancel()
	go func() {
		select {
		case <-done:
			cancel()
		case <-ctx.Done():
		}
	}()
	connectCtx, stopConnect := context.WithTimeout(ctx, 10*time.Second)
	defer stopConnect()
	var connection net.Conn
	for connectCtx.Err() == nil {
		var err error
		connection, err = (&net.Dialer{Timeout: time.Second}).DialContext(connectCtx, "tcp4", session.address)
		if err == nil {
			break
		}
		select {
		case <-time.After(100 * time.Millisecond):
		case <-connectCtx.Done():
		}
	}
	if connection != nil {
		defer connection.Close()
		stop := context.AfterFunc(ctx, func() { connection.Close() })
		defer stop()
		_ = connection.SetDeadline(time.Now().Add(10 * time.Second))
		_, err := fmt.Fprintf(connection, "%s\n", session.password)
		if err == nil {
			scanner := bufio.NewScanner(connection)
			scanner.Buffer(make([]byte, 4096), 16<<10)
			authenticated := false
			for scanner.Scan() {
				line := scanner.Text()
				if !authenticated {
					line = strings.TrimPrefix(line, "ENTER PASSWORD:")
					if line != "SUCCESS: password is correct" {
						continue
					}
					authenticated = true
					if err := connection.SetDeadline(time.Time{}); err != nil {
						break
					}
					if _, err := fmt.Fprint(connection, "state on\nstate\n"); err != nil {
						break
					}
					continue
				}
				if strings.HasPrefix(line, ">HOLD:") {
					if _, err := fmt.Fprint(connection, "hold release\n"); err != nil {
						break
					}
					continue
				}
				if state, ip, ok := parseManagementState(line); ok {
					name := ""
					if state == ipc.StateConnected {
						readyCtx, stopReady := context.WithTimeout(ctx, 5*time.Second)
						for readyCtx.Err() == nil {
							var actualIP string
							name, actualIP, err = netcfg.AdapterAddress(session.adapterGUID, ip)
							if err == nil && actualIP == ip {
								break
							}
							name = ""
							select {
							case <-time.After(100 * time.Millisecond):
							case <-readyCtx.Done():
							}
						}
						stopReady()
						if name == "" {
							break
						}
					}
					m.applyManagementState(generation, state, ip, name)
				}
			}
		}
	}
	if ctx.Err() != nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.generation == generation && m.process != nil && !m.stopWanted {
		m.status = ipc.Status{State: ipc.StateError, CredentialID: m.status.CredentialID, ErrorCode: "E_OPENVPN_MANAGEMENT"}
		_ = m.process.Process.Kill()
	}
}

func parseManagementState(line string) (ipc.ConnectionState, string, bool) {
	if strings.HasPrefix(line, ">STATE:") {
		line = strings.TrimPrefix(line, ">STATE:")
	}
	fields := strings.Split(line, ",")
	if len(fields) < 4 {
		return "", "", false
	}
	if _, err := strconv.ParseInt(fields[0], 10, 64); err != nil {
		return "", "", false
	}
	switch fields[1] {
	case "CONNECTED":
		if fields[2] != "SUCCESS" || net.ParseIP(fields[3]) == nil {
			return "", "", false
		}
		return ipc.StateConnected, fields[3], true
	case "INITIAL", "CONNECTING", "WAIT", "AUTH", "GET_CONFIG", "ASSIGN_IP", "ADD_ROUTES", "RECONNECTING", "RESOLVE", "TCP_CONNECT":
		return ipc.StateConnecting, "", true
	case "EXITING":
		return ipc.StateDisconnected, "", true
	}
	return "", "", false
}

func (m *Manager) applyManagementState(generation uint64, state ipc.ConnectionState, ip, adapter string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.generation != generation || m.process == nil || m.stopWanted {
		return
	}
	m.status = ipc.Status{State: state, CredentialID: m.status.CredentialID, VirtualIP: ip, AdapterName: adapter}
}
