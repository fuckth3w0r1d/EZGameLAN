package join

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/r3t2/ezgamelan/internal/atomicfile"
	"github.com/r3t2/ezgamelan/internal/invite"
	"github.com/r3t2/ezgamelan/internal/openvpn"
)

const (
	MaxCredentials     = 32
	credentialIDLength = 32
)

// Credential describes a stored identity without exposing private key material.
type Credential struct {
	ID         string
	ClientName string
	Endpoint   string
	ServerName string
}

// CredentialStore stores one protected JSON profile per identity.
type CredentialStore struct {
	ownerSID    string
	profilesDir string
}

func NewCredentialStore(profilesDir, ownerSID string) (*CredentialStore, error) {
	if !filepath.IsAbs(profilesDir) {
		return nil, errors.New("E_INSTALL_PATH: 凭证目录必须是绝对路径")
	}
	if ownerSID == "" {
		return nil, errors.New("E_INSTALL_PERMISSIONS: 凭证所有者不能为空")
	}
	if err := validateOwnerSID(ownerSID); err != nil {
		return nil, fmt.Errorf("E_INSTALL_PERMISSIONS: 凭证所有者无效: %w", err)
	}
	return &CredentialStore{profilesDir: profilesDir, ownerSID: ownerSID}, nil
}

// PrepareJoin checks local storage before consuming a one-time invitation.
func (s *CredentialStore) PrepareJoin(ctx context.Context) error {
	credentials, err := s.List()
	if err != nil {
		return err
	}
	if len(credentials) >= MaxCredentials {
		return errors.New("E_CREDENTIAL_LIMIT: 请先删除不用的凭证，留出领取空间")
	}
	if err := os.MkdirAll(s.profilesDir, 0o700); err != nil {
		return err
	}
	if err := ProtectPath(ctx, s.profilesDir, true, s.ownerSID); err != nil {
		return err
	}
	f, err := os.CreateTemp(s.profilesDir, ".write-check-*")
	if err != nil {
		return fmt.Errorf("E_CREDENTIAL_WRITE: 凭证目录不可写: %w", err)
	}
	defer os.Remove(f.Name())
	err = f.Sync()
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func (s *CredentialStore) List() ([]Credential, error) {
	entries, err := readProfilesDirectory(s.profilesDir)
	if err != nil {
		return nil, err
	}
	credentials := make([]Credential, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if filepath.Ext(name) != ".json" {
			continue
		}
		profile, err := s.readProfile(name[:len(name)-len(".json")])
		if err != nil {
			return nil, err
		}
		credentials = append(credentials, credentialForProfile(profile))
		if len(credentials) > MaxCredentials {
			return nil, errors.New("E_CREDENTIAL_LIMIT: 本机保存的凭证过多")
		}
	}
	sort.Slice(credentials, func(i, j int) bool {
		left, right := credentials[i], credentials[j]
		if left.Endpoint != right.Endpoint {
			return left.Endpoint < right.Endpoint
		}
		if left.ServerName != right.ServerName {
			return left.ServerName < right.ServerName
		}
		if left.ClientName != right.ClientName {
			return left.ClientName < right.ClientName
		}
		return left.ID < right.ID
	})
	return credentials, nil
}

func (s *CredentialStore) Install(ctx context.Context, profile invite.Profile) (Credential, error) {
	data, err := invite.Marshal(profile, time.Now())
	if err != nil {
		return Credential{}, fmt.Errorf("E_JOIN_CONFIG: 凭证校验失败: %w", err)
	}
	credential := credentialForProfile(profile)
	path := s.profilePath(credential.ID)
	exists, err := regularFileExists(path)
	if err != nil {
		return Credential{}, fmt.Errorf("E_CREDENTIAL_WRITE: 检查本机凭证: %w", err)
	}
	if !exists {
		credentials, err := s.List()
		if err != nil {
			return Credential{}, err
		}
		if len(credentials) >= MaxCredentials {
			return Credential{}, errors.New("E_CREDENTIAL_LIMIT: 本机最多保存 32 个凭证")
		}
	}
	if err := os.MkdirAll(s.profilesDir, 0o700); err != nil {
		return Credential{}, fmt.Errorf("E_CREDENTIAL_WRITE: 创建凭证目录: %w", err)
	}
	// Temporary files inherit access for the owner, SYSTEM and Administrators.
	if err := ProtectPath(ctx, s.profilesDir, true, s.ownerSID); err != nil {
		return Credential{}, fmt.Errorf("E_INSTALL_PERMISSIONS: 保护凭证目录: %w", err)
	}
	if err := atomicfile.Replace(path, data, 0o600); err != nil {
		return Credential{}, fmt.Errorf("E_CREDENTIAL_WRITE: 保存凭证: %w", err)
	}
	return credential, nil
}

func (s *CredentialStore) Delete(id string) error {
	if !ValidCredentialID(id) {
		return errors.New("E_CREDENTIAL_ID: 凭证 ID 无效")
	}
	path := s.profilePath(id)
	exists, err := regularFileExists(path)
	if err != nil {
		return fmt.Errorf("E_CREDENTIAL_DELETE: 检查本机凭证: %w", err)
	}
	if !exists {
		return errors.New("E_CREDENTIAL_NOT_FOUND: 所选凭证不存在")
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("E_CREDENTIAL_DELETE: 删除本机凭证: %w", err)
	}
	return nil
}

func (s *CredentialStore) profilePath(id string) string {
	return filepath.Join(s.profilesDir, id+".json")
}

func (s *CredentialStore) readProfile(id string) (invite.Profile, error) {
	if !ValidCredentialID(id) {
		return invite.Profile{}, errors.New("E_CREDENTIAL_ID: 凭证 ID 无效")
	}
	path := s.profilePath(id)
	exists, err := regularFileExists(path)
	if err != nil {
		return invite.Profile{}, fmt.Errorf("E_CREDENTIAL_READ: 检查本机凭证: %w", err)
	}
	if !exists {
		return invite.Profile{}, errors.New("E_CREDENTIAL_NOT_FOUND: 所选凭证不存在")
	}
	file, err := os.Open(path)
	if err != nil {
		return invite.Profile{}, fmt.Errorf("E_CREDENTIAL_READ: 打开本机凭证: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, invite.MaxSize+1))
	if err != nil {
		return invite.Profile{}, fmt.Errorf("E_CREDENTIAL_READ: 读取本机凭证: %w", err)
	}
	profile, err := invite.Decode(data)
	if err != nil {
		return invite.Profile{}, fmt.Errorf("E_CREDENTIAL_READ: 解析本机凭证: %w", err)
	}
	if credentialForProfile(profile).ID != id {
		return invite.Profile{}, errors.New("E_CREDENTIAL_READ: 凭证 ID 与文件名不匹配")
	}
	return profile, nil
}

// RuntimeConfig validates the identity and generates a fixed OpenVPN configuration.
func (s *CredentialStore) RuntimeConfig(id, directory string) (string, error) {
	profile, err := s.readProfile(id)
	if err != nil {
		return "", err
	}
	config, err := openvpn.ClientConfig(profile, 1400)
	if err != nil {
		return "", fmt.Errorf("E_CREDENTIAL_READ: %w", err)
	}
	path := filepath.Join(directory, "client.ovpn")
	return path, atomicfile.Replace(path, []byte(config), 0o600)
}

func credentialForProfile(profile invite.Profile) Credential {
	return Credential{
		ID:         credentialID(profile.Endpoint, profile.ServerName, profile.ClientName),
		ClientName: profile.ClientName,
		Endpoint:   profile.Endpoint,
		ServerName: profile.ServerName,
	}
}

func credentialID(endpoint, serverName, clientName string) string {
	sum := sha256.Sum256([]byte(endpoint + "\x00" + serverName + "\x00" + clientName))
	return hex.EncodeToString(sum[:credentialIDLength/2])
}

// ValidCredentialID reports whether id can address a stored profile.
func ValidCredentialID(id string) bool {
	if len(id) != credentialIDLength {
		return false
	}
	for _, character := range id {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func readProfilesDirectory(path string) ([]os.DirEntry, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("E_CREDENTIAL_READ: 检查凭证目录: %w", err)
	}
	if !info.IsDir() {
		return nil, errors.New("E_CREDENTIAL_READ: 凭证目录无效")
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, fmt.Errorf("E_CREDENTIAL_READ: 读取凭证目录: %w", err)
	}
	return entries, nil
}

func regularFileExists(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, errors.New("路径不是普通文件")
	}
	return true, nil
}
