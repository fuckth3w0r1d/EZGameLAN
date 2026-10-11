//go:build windows

package netcfg

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var adapterGUIDPattern = regexp.MustCompile(`(?i)\{[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\}`)

func EnsureDedicatedAdapter(ctx context.Context, ownerSID, tapctl string) (string, error) {
	if _, err := windows.StringToSid(ownerSID); err != nil {
		return "", err
	}
	key, _, err := registry.CreateKey(registry.LOCAL_MACHINE, `SOFTWARE\EZGameLAN\Adapters`, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return "", err
	}
	defer key.Close()
	guid, _, err := key.GetStringValue(ownerSID)
	if err == nil {
		if adapterGUIDPattern.FindString(guid) != guid {
			return "", errors.New("invalid owned adapter GUID")
		}
		name, _, err := AdapterAddress(guid, "")
		if err != nil {
			return "", err
		}
		if name != "" {
			return guid, nil
		}
	} else if !errors.Is(err, registry.ErrNotExist) {
		return "", err
	}
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		return "", err
	}
	command := exec.CommandContext(ctx, tapctl, "create", "--hwid", "tap0901", "--name", "EZGameLAN-"+hex.EncodeToString(suffix))
	command.SysProcAttr = &windows.SysProcAttr{HideWindow: true}
	command.Dir = filepath.Dir(tapctl)
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("create dedicated TAP adapter: %w: %s", err, strings.TrimSpace(string(output)))
	}
	guid = adapterGUIDPattern.FindString(string(output))
	if guid == "" {
		return "", errors.New("tapctl did not return an adapter GUID")
	}
	if err := key.SetStringValue(ownerSID, guid); err != nil {
		cleanup := exec.CommandContext(ctx, tapctl, "delete", guid)
		cleanup.SysProcAttr = &windows.SysProcAttr{HideWindow: true}
		_ = cleanup.Run()
		return "", err
	}
	return guid, nil
}

// AdapterAddress checks only the recorded TAP instance and the negotiated IP.
func AdapterAddress(guid, expectedIP string) (string, string, error) {
	size := uint32(16 << 10)
	for attempts := 0; attempts < 3; attempts++ {
		buffer := make([]byte, size)
		first := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buffer[0]))
		err := windows.GetAdaptersAddresses(windows.AF_INET, 0, 0, first, &size)
		if errors.Is(err, windows.ERROR_BUFFER_OVERFLOW) {
			continue
		}
		if err != nil {
			return "", "", err
		}
		for adapter := first; adapter != nil; adapter = adapter.Next {
			if !strings.EqualFold(windows.BytePtrToString(adapter.AdapterName), guid) {
				continue
			}
			if !strings.Contains(windows.UTF16PtrToString(adapter.Description), "TAP-Windows") {
				return "", "", errors.New("owned adapter is not TAP-Windows")
			}
			name := windows.UTF16PtrToString(adapter.FriendlyName)
			if adapter.OperStatus != 1 {
				return name, "", nil
			}
			for address := adapter.FirstUnicastAddress; address != nil; address = address.Next {
				ip := address.Address.IP()
				if ip != nil && ip.Equal(net.ParseIP(expectedIP)) {
					return name, ip.String(), nil
				}
			}
			return name, "", nil
		}
		return "", "", nil
	}
	return "", "", errors.New("adapter list changed repeatedly")
}
