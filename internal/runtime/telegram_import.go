package runtime

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	gotdcrypto "github.com/gotd/td/crypto"
	"github.com/gotd/td/session"

	"tgworkbench/internal/connector"
	"tgworkbench/internal/domain"
)

const (
	maxTelegramImportBytes   = 2 << 20
	maxTelegramImportEntries = 8
)

type telethonImport struct {
	phone   string
	apiID   int
	apiHash string
	session session.Data
}

type telethonMetadata struct {
	Phone   string          `json:"phone"`
	APIID   json.RawMessage `json:"app_id"`
	APIHash string          `json:"app_hash"`
}

func (m *Manager) ImportSession(input domain.AccountSessionImport) (domain.Account, error) {
	input.Name = strings.TrimSpace(input.Name)
	if input.Platform == "" {
		input.Platform = connector.Telegram
	}
	if input.Platform != connector.Telegram {
		return domain.Account{}, connector.InputError{Message: "Telegram Connector 不能导入其他平台会话"}
	}
	if input.Name == "" {
		return domain.Account{}, connector.InputError{Message: "账号备注不能为空"}
	}
	if !strings.EqualFold(filepath.Ext(input.Filename), ".zip") {
		return domain.Account{}, connector.InputError{Message: "只支持 Telethon 协议 ZIP"}
	}

	parsed, err := parseTelethonZIP(input.Data, m.dataDir)
	if err != nil {
		return domain.Account{}, connector.InputError{Message: "协议包校验失败: " + err.Error()}
	}
	account, err := m.CreateAccount(domain.AccountInput{
		Platform: connector.Telegram,
		Name:     input.Name,
		Phone:    parsed.phone,
		APIID:    parsed.apiID,
		APIHash:  parsed.apiHash,
	})
	if err != nil {
		return domain.Account{}, err
	}

	storage := &encryptedSessionStorage{
		path:  filepath.Join(m.dataDir, "sessions", account.ID+".session"),
		vault: m.vault,
	}
	loader := session.Loader{Storage: storage}
	if err := loader.Save(context.Background(), &parsed.session); err != nil {
		_ = m.store.DeleteAccount(account.ID)
		_ = os.Remove(storage.path)
		return domain.Account{}, fmt.Errorf("保存加密会话: %w", err)
	}
	return account, nil
}

func parseTelethonZIP(data []byte, tempDir string) (telethonImport, error) {
	if len(data) == 0 || len(data) > maxTelegramImportBytes {
		return telethonImport{}, errors.New("ZIP 为空或超过 2 MiB")
	}
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return telethonImport{}, errors.New("不是有效的 ZIP 文件")
	}
	if len(reader.File) == 0 || len(reader.File) > maxTelegramImportEntries {
		return telethonImport{}, errors.New("ZIP 文件数量无效")
	}

	var sessionBytes, metadataBytes []byte
	var total uint64
	for _, entry := range reader.File {
		if err := validateZIPEntry(entry); err != nil {
			return telethonImport{}, err
		}
		if entry.FileInfo().IsDir() {
			continue
		}
		total += entry.UncompressedSize64
		if total > maxTelegramImportBytes {
			return telethonImport{}, errors.New("ZIP 解压内容超过 2 MiB")
		}
		content, err := readZIPEntry(entry)
		if err != nil {
			return telethonImport{}, err
		}
		switch strings.ToLower(path.Ext(entry.Name)) {
		case ".session":
			if sessionBytes != nil {
				return telethonImport{}, errors.New("ZIP 只能包含一个 .session 文件")
			}
			sessionBytes = content
		case ".json":
			if metadataBytes != nil {
				return telethonImport{}, errors.New("ZIP 只能包含一个 .json 文件")
			}
			metadataBytes = content
		default:
			return telethonImport{}, fmt.Errorf("ZIP 包含不允许的文件类型 %q", path.Ext(entry.Name))
		}
	}
	if len(sessionBytes) == 0 || len(metadataBytes) == 0 {
		return telethonImport{}, errors.New("ZIP 必须包含一个 .session 和一个 .json 文件")
	}

	metadata, err := parseTelethonMetadata(metadataBytes)
	if err != nil {
		return telethonImport{}, err
	}
	dataDir := tempDir
	if strings.TrimSpace(dataDir) == "" {
		dataDir = os.TempDir()
	}
	temp, err := os.CreateTemp(dataDir, "telethon-import-*.session")
	if err != nil {
		return telethonImport{}, fmt.Errorf("创建临时校验文件: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return telethonImport{}, fmt.Errorf("限制临时文件权限: %w", err)
	}
	if _, err := temp.Write(sessionBytes); err != nil {
		temp.Close()
		return telethonImport{}, fmt.Errorf("写入临时校验文件: %w", err)
	}
	if err := temp.Close(); err != nil {
		return telethonImport{}, fmt.Errorf("关闭临时校验文件: %w", err)
	}
	parsedSession, err := readTelethonSQLite(tempPath)
	if err != nil {
		return telethonImport{}, err
	}
	return telethonImport{phone: metadata.Phone, apiID: metadata.APIID, apiHash: metadata.APIHash, session: parsedSession}, nil
}

func validateZIPEntry(entry *zip.File) error {
	name := strings.ReplaceAll(entry.Name, "\\", "/")
	clean := path.Clean(name)
	if name == "" || strings.HasPrefix(name, "/") || clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(name, ":") {
		return errors.New("ZIP 包含不安全路径")
	}
	if entry.UncompressedSize64 > maxTelegramImportBytes {
		return errors.New("ZIP 条目过大")
	}
	if entry.Flags&1 != 0 {
		return errors.New("不支持加密 ZIP")
	}
	return nil
}

func readZIPEntry(entry *zip.File) ([]byte, error) {
	reader, err := entry.Open()
	if err != nil {
		return nil, fmt.Errorf("读取 ZIP 条目: %w", err)
	}
	defer reader.Close()
	content, err := io.ReadAll(io.LimitReader(reader, maxTelegramImportBytes+1))
	if err != nil || len(content) > maxTelegramImportBytes {
		return nil, errors.New("ZIP 条目读取失败或过大")
	}
	return content, nil
}

func parseTelethonMetadata(data []byte) (struct {
	Phone   string
	APIID   int
	APIHash string
}, error) {
	var raw telethonMetadata
	if err := json.Unmarshal(data, &raw); err != nil {
		return struct {
			Phone   string
			APIID   int
			APIHash string
		}{}, errors.New("JSON 元数据无效")
	}
	apiID, err := parseImportAPIID(raw.APIID)
	if err != nil {
		return struct {
			Phone   string
			APIID   int
			APIHash string
		}{}, err
	}
	result := struct {
		Phone   string
		APIID   int
		APIHash string
	}{Phone: strings.TrimSpace(raw.Phone), APIID: apiID, APIHash: strings.TrimSpace(raw.APIHash)}
	if result.Phone == "" || result.APIID <= 0 || len(result.APIHash) < 8 {
		return result, errors.New("JSON 缺少有效的 phone、app_id 或 app_hash")
	}
	return result, nil
}

func parseImportAPIID(raw json.RawMessage) (int, error) {
	var value int
	if err := json.Unmarshal(raw, &value); err == nil && value > 0 {
		return value, nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		value, err = strconv.Atoi(strings.TrimSpace(text))
		if err == nil && value > 0 {
			return value, nil
		}
	}
	return 0, errors.New("JSON 中的 app_id 无效")
}

func readTelethonSQLite(filename string) (session.Data, error) {
	db, err := sql.Open("sqlite", filename+"?mode=ro")
	if err != nil {
		return session.Data{}, errors.New("无法打开 Telethon Session")
	}
	defer db.Close()
	var integrity string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		return session.Data{}, errors.New("Session SQLite 完整性校验失败")
	}
	if err := validateTelethonSchema(db); err != nil {
		return session.Data{}, err
	}
	rows, err := db.Query("SELECT dc_id, server_address, port, auth_key FROM sessions LIMIT 2")
	if err != nil {
		return session.Data{}, errors.New("Session 缺少有效的 sessions 表")
	}
	defer rows.Close()
	var dcID, port int
	var server string
	var authKey []byte
	count := 0
	for rows.Next() {
		count++
		if err := rows.Scan(&dcID, &server, &port, &authKey); err != nil {
			return session.Data{}, errors.New("Session 数据格式无效")
		}
	}
	if err := rows.Err(); err != nil || count != 1 {
		return session.Data{}, errors.New("Session 必须且只能包含一个授权记录")
	}
	server = strings.TrimSpace(server)
	if dcID < 1 || dcID > 5 || port < 1 || port > 65535 || !validTelegramServer(server) || len(authKey) != len(gotdcrypto.Key{}) {
		return session.Data{}, errors.New("Session 的 DC、服务器、端口或授权密钥无效")
	}
	var key gotdcrypto.Key
	copy(key[:], authKey)
	keyID := key.WithID().ID
	return session.Data{DC: dcID, Addr: net.JoinHostPort(server, strconv.Itoa(port)), AuthKey: key[:], AuthKeyID: keyID[:]}, nil
}

func validateTelethonSchema(db *sql.DB) error {
	rows, err := db.Query("PRAGMA table_info(sessions)")
	if err != nil {
		return errors.New("无法读取 Session 表结构")
	}
	defer rows.Close()
	required := map[string]bool{"dc_id": false, "server_address": false, "port": false, "auth_key": false}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, kind string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
			return errors.New("Session 表结构无效")
		}
		if _, ok := required[name]; ok {
			required[name] = true
		}
	}
	for _, present := range required {
		if !present {
			return errors.New("Session 不是标准 Telethon SQLite 格式")
		}
	}
	return nil
}

func validTelegramServer(server string) bool {
	if ip := net.ParseIP(server); ip != nil {
		return true
	}
	if len(server) == 0 || len(server) > 253 || strings.HasPrefix(server, ".") || strings.HasSuffix(server, ".") {
		return false
	}
	for _, label := range strings.Split(server, ".") {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
		for _, char := range label {
			if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '-' {
				return false
			}
		}
	}
	return true
}
