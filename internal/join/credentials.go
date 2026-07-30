package join

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/r3t2/ezgamelan/internal/atomicfile"
	"github.com/r3t2/ezgamelan/internal/invite"
)

const (
	LegacyCredentialID       = "legacy"
	MaxCredentials           = 32
	credentialMetadataFormat = 1
	credentialMetadataLimit  = 4 << 10
	credentialIDLength       = 32
	profilesDirectoryName    = "profiles"
)

// Credential describes one locally held OpenVPN identity without exposing its
// private configuration.
type Credential struct {
	ID         string
	ClientName string
	Endpoint   string
	ServerName string
	Legacy     bool
}

type credentialMetadata struct {
	FormatVersion int    `json:"format_version"`
	ID            string `json:"id"`
	ClientName    string `json:"client_name"`
	Endpoint      string `json:"endpoint"`
	ServerName    string `json:"server_name"`
}

// CredentialStore owns the protected client configurations beneath the
// existing EZGameLAN config directory.
type CredentialStore struct {
	legacyConfigPath string
	profilesDir      string
}

func NewCredentialStore(legacyConfigPath string) (*CredentialStore, error) {
	if !filepath.IsAbs(legacyConfigPath) {
		return nil, errors.New("E_INSTALL_PATH: 凭证配置路径必须是绝对路径")
	}
	return &CredentialStore{
		legacyConfigPath: legacyConfigPath,
		profilesDir: filepath.Join(
			filepath.Dir(legacyConfigPath),
			profilesDirectoryName,
		),
	}, nil
}

func (s *CredentialStore) List() ([]Credential, error) {
	credentials := make([]Credential, 0, MaxCredentials+1)
	if exists, err := regularFileExists(s.legacyConfigPath); err != nil {
		return nil, fmt.Errorf("E_CREDENTIAL_READ: 检查旧版凭证: %w", err)
	} else if exists {
		credentials = append(credentials, Credential{
			ID:     LegacyCredentialID,
			Legacy: true,
		})
	}

	entries, err := readProfilesDirectory(s.profilesDir)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		name := entry.Name()
		if filepath.Ext(name) != ".json" {
			continue
		}
		info, err := os.Lstat(filepath.Join(s.profilesDir, name))
		if err != nil {
			return nil, fmt.Errorf("E_CREDENTIAL_READ: 检查凭证元数据: %w", err)
		}
		if !info.Mode().IsRegular() {
			return nil, errors.New("E_CREDENTIAL_READ: 凭证元数据不是普通文件")
		}
		metadata, err := s.readMetadata(filepath.Join(s.profilesDir, name))
		if err != nil {
			return nil, err
		}
		if name != metadata.ID+".json" {
			return nil, errors.New("E_CREDENTIAL_READ: 凭证元数据文件名不匹配")
		}
		configPath := s.profileConfigPath(metadata.ID)
		if exists, err := regularFileExists(configPath); err != nil {
			return nil, fmt.Errorf("E_CREDENTIAL_READ: 检查凭证配置: %w", err)
		} else if !exists {
			return nil, errors.New("E_CREDENTIAL_READ: 凭证配置缺失")
		}
		credentials = append(credentials, metadata.credential())
	}
	profileCount := len(credentials)
	if profileCount > 0 && credentials[0].Legacy {
		profileCount--
	}
	if profileCount > MaxCredentials {
		return nil, errors.New("E_CREDENTIAL_LIMIT: 本机保存的凭证过多")
	}
	sort.Slice(credentials, func(i, j int) bool {
		left, right := credentials[i], credentials[j]
		if left.Legacy != right.Legacy {
			return left.Legacy
		}
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

func (s *CredentialStore) Install(
	ctx context.Context,
	profile invite.Profile,
) (Credential, error) {
	metadata := metadataForProfile(profile)
	if err := validateMetadata(metadata); err != nil {
		return Credential{}, fmt.Errorf("E_JOIN_CONFIG: 凭证元数据无效: %w", err)
	}
	metadataPath := s.profileMetadataPath(metadata.ID)
	configPath := s.profileConfigPath(metadata.ID)

	metadataExists, err := regularFileExists(metadataPath)
	if err != nil {
		return Credential{}, fmt.Errorf("E_CREDENTIAL_WRITE: 检查凭证元数据: %w", err)
	}
	if !metadataExists {
		credentials, err := s.List()
		if err != nil {
			return Credential{}, err
		}
		count := 0
		for _, credential := range credentials {
			if !credential.Legacy {
				count++
			}
		}
		if count >= MaxCredentials {
			return Credential{}, errors.New("E_CREDENTIAL_LIMIT: 本机最多保存 32 个凭证")
		}
	}

	previousConfig, configExists, err := readOptionalRegular(configPath, maxConfigSize)
	if err != nil {
		return Credential{}, fmt.Errorf("E_CREDENTIAL_WRITE: 读取现有凭证配置: %w", err)
	}
	previousMetadata, _, err := readOptionalRegular(metadataPath, credentialMetadataLimit)
	if err != nil {
		return Credential{}, fmt.Errorf("E_CREDENTIAL_WRITE: 读取现有凭证元数据: %w", err)
	}

	if _, err := Install(ctx, profile, configPath); err != nil {
		return Credential{}, err
	}
	data, err := json.Marshal(metadata)
	if err != nil {
		s.rollbackConfig(ctx, configPath, previousConfig, configExists)
		return Credential{}, fmt.Errorf("E_CREDENTIAL_WRITE: 编码凭证元数据: %w", err)
	}
	data = append(data, '\n')
	if err := atomicfile.Replace(metadataPath, data, 0o600); err != nil {
		s.rollbackConfig(ctx, configPath, previousConfig, configExists)
		return Credential{}, fmt.Errorf("E_CREDENTIAL_WRITE: 保存凭证元数据: %w", err)
	}
	if err := securePath(ctx, metadataPath, false); err != nil {
		s.rollbackConfig(ctx, configPath, previousConfig, configExists)
		restoreOptionalFile(ctx, metadataPath, previousMetadata, metadataExists)
		return Credential{}, fmt.Errorf("E_INSTALL_PERMISSIONS: 保护凭证元数据: %w", err)
	}
	return metadata.credential(), nil
}

func (s *CredentialStore) ConfigPath(id string) (string, error) {
	if id == LegacyCredentialID {
		if exists, err := regularFileExists(s.legacyConfigPath); err != nil {
			return "", fmt.Errorf("E_CREDENTIAL_READ: 检查旧版凭证: %w", err)
		} else if !exists {
			return "", errors.New("E_CREDENTIAL_NOT_FOUND: 旧版凭证不存在")
		}
		return s.legacyConfigPath, nil
	}
	if !validCredentialID(id) {
		return "", errors.New("E_CREDENTIAL_ID: 凭证 ID 无效")
	}
	metadata, err := s.readMetadata(s.profileMetadataPath(id))
	if err != nil {
		return "", err
	}
	if metadata.ID != id {
		return "", errors.New("E_CREDENTIAL_READ: 凭证 ID 不匹配")
	}
	configPath := s.profileConfigPath(id)
	if exists, err := regularFileExists(configPath); err != nil {
		return "", fmt.Errorf("E_CREDENTIAL_READ: 检查凭证配置: %w", err)
	} else if !exists {
		return "", errors.New("E_CREDENTIAL_NOT_FOUND: 凭证配置不存在")
	}
	return configPath, nil
}

func (s *CredentialStore) Delete(id string) error {
	if !ValidCredentialID(id) {
		return errors.New("E_CREDENTIAL_ID: 凭证 ID 无效")
	}

	configPath := s.legacyConfigPath
	paths := []string{configPath, configPath + ".bak"}
	if id != LegacyCredentialID {
		configPath = s.profileConfigPath(id)
		paths = []string{s.profileMetadataPath(id), configPath, configPath + ".bak"}
	}

	existing := 0
	metadataExists := false
	for index, path := range paths {
		exists, err := regularFileExists(path)
		if err != nil {
			return fmt.Errorf("E_CREDENTIAL_DELETE: 检查本机凭证: %w", err)
		}
		if exists {
			existing++
			if id != LegacyCredentialID && index == 0 {
				metadataExists = true
			}
		}
	}
	if existing == 0 {
		return errors.New("E_CREDENTIAL_NOT_FOUND: 所选凭证不存在")
	}
	if metadataExists {
		metadata, err := s.readMetadata(paths[0])
		if err != nil {
			return err
		}
		if metadata.ID != id {
			return errors.New("E_CREDENTIAL_READ: 凭证 ID 不匹配")
		}
	}

	for _, path := range paths {
		if err := removeRegularFile(path); err != nil {
			return fmt.Errorf("E_CREDENTIAL_DELETE: 删除本机凭证: %w", err)
		}
	}
	return nil
}

func (s *CredentialStore) profileConfigPath(id string) string {
	return filepath.Join(s.profilesDir, id+".ovpn")
}

func (s *CredentialStore) profileMetadataPath(id string) string {
	return filepath.Join(s.profilesDir, id+".json")
}

func (s *CredentialStore) readMetadata(path string) (credentialMetadata, error) {
	data, err := readBounded(path, credentialMetadataLimit)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return credentialMetadata{}, errors.New("E_CREDENTIAL_NOT_FOUND: 凭证元数据不存在")
		}
		return credentialMetadata{}, fmt.Errorf("E_CREDENTIAL_READ: 读取凭证元数据: %w", err)
	}
	metadata, err := decodeCredentialMetadata(data)
	if err != nil {
		return credentialMetadata{}, fmt.Errorf("E_CREDENTIAL_READ: 解析凭证元数据: %w", err)
	}
	if err := validateMetadata(metadata); err != nil {
		return credentialMetadata{}, fmt.Errorf("E_CREDENTIAL_READ: 校验凭证元数据: %w", err)
	}
	return metadata, nil
}

func (s *CredentialStore) rollbackConfig(
	ctx context.Context,
	path string,
	previous []byte,
	existed bool,
) {
	restoreOptionalFile(ctx, path, previous, existed)
}

func restoreOptionalFile(ctx context.Context, path string, data []byte, existed bool) {
	if !existed {
		_ = os.Remove(path)
		return
	}
	_ = atomicfile.Replace(path, data, 0o600)
	_ = securePath(ctx, path, false)
}

func metadataForProfile(profile invite.Profile) credentialMetadata {
	id := credentialID(profile.Endpoint, profile.ServerName, profile.ClientName)
	return credentialMetadata{
		FormatVersion: credentialMetadataFormat,
		ID:            id,
		ClientName:    profile.ClientName,
		Endpoint:      profile.Endpoint,
		ServerName:    profile.ServerName,
	}
}

func (m credentialMetadata) credential() Credential {
	return Credential{
		ID:         m.ID,
		ClientName: m.ClientName,
		Endpoint:   m.Endpoint,
		ServerName: m.ServerName,
	}
}

func credentialID(endpoint, serverName, clientName string) string {
	sum := sha256.Sum256([]byte(endpoint + "\x00" + serverName + "\x00" + clientName))
	return hex.EncodeToString(sum[:credentialIDLength/2])
}

func validCredentialID(id string) bool {
	if len(id) != credentialIDLength {
		return false
	}
	for _, character := range id {
		if (character < '0' || character > '9') &&
			(character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

// ValidCredentialID reports whether id can address a stored or legacy profile.
func ValidCredentialID(id string) bool {
	return id == LegacyCredentialID || validCredentialID(id)
}

func validateMetadata(metadata credentialMetadata) error {
	if metadata.FormatVersion != credentialMetadataFormat {
		return errors.New("元数据版本不受支持")
	}
	if !validCredentialID(metadata.ID) {
		return errors.New("凭证 ID 无效")
	}
	if !invite.ValidName(metadata.ClientName) {
		return errors.New("设备名称无效")
	}
	if !invite.ValidNameWithLimit(metadata.ServerName, 64) {
		return errors.New("服务端名称无效")
	}
	host, portText, err := net.SplitHostPort(metadata.Endpoint)
	if err != nil || host == "" || len(metadata.Endpoint) > 512 {
		return errors.New("服务端地址无效")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return errors.New("服务端端口无效")
	}
	wantID := credentialID(metadata.Endpoint, metadata.ServerName, metadata.ClientName)
	if metadata.ID != wantID {
		return errors.New("凭证 ID 与元数据不匹配")
	}
	return nil
}

func decodeCredentialMetadata(data []byte) (credentialMetadata, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return credentialMetadata{}, err
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return credentialMetadata{}, errors.New("凭证元数据必须是 JSON 对象")
	}
	var metadata credentialMetadata
	seen := make(map[string]struct{}, 5)
	for decoder.More() {
		nameToken, err := decoder.Token()
		if err != nil {
			return credentialMetadata{}, err
		}
		name, ok := nameToken.(string)
		if !ok {
			return credentialMetadata{}, errors.New("凭证元数据字段名无效")
		}
		if _, exists := seen[name]; exists {
			return credentialMetadata{}, fmt.Errorf("凭证元数据字段 %q 重复", name)
		}
		seen[name] = struct{}{}
		switch name {
		case "format_version":
			err = decoder.Decode(&metadata.FormatVersion)
		case "id":
			err = decoder.Decode(&metadata.ID)
		case "client_name":
			err = decoder.Decode(&metadata.ClientName)
		case "endpoint":
			err = decoder.Decode(&metadata.Endpoint)
		case "server_name":
			err = decoder.Decode(&metadata.ServerName)
		default:
			return credentialMetadata{}, fmt.Errorf("未知凭证元数据字段 %q", name)
		}
		if err != nil {
			return credentialMetadata{}, fmt.Errorf("解析凭证元数据字段 %q: %w", name, err)
		}
	}
	if _, err := decoder.Token(); err != nil {
		return credentialMetadata{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return credentialMetadata{}, errors.New("凭证元数据包含多余 JSON 内容")
		}
		return credentialMetadata{}, err
	}
	return metadata, nil
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

func readOptionalRegular(path string, limit int64) ([]byte, bool, error) {
	exists, err := regularFileExists(path)
	if err != nil || !exists {
		return nil, exists, err
	}
	data, err := readBounded(path, limit)
	return data, true, err
}

func removeRegularFile(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("待删除路径不是普通文件")
	}
	return os.Remove(path)
}
