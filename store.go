package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/rob121/cannon/extension"
	"golang.org/x/oauth2"
	"gorm.io/gorm"
)

type scheduleSettings struct {
	Enabled     bool   `json:"enabled"`
	Frequency   string `json:"frequency"`
	RetainCount int    `json:"retain_count"`
}

type connectionSettings struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	TokenJSON    string `json:"token_json"`
	FolderID     string `json:"folder_id"`
	FolderName   string `json:"folder_name"`
	AccountEmail string `json:"account_email"`
}

type backupLog struct {
	ID           uint      `gorm:"primaryKey;column:log_id" json:"id"`
	StartedAt    time.Time `gorm:"column:started_at;index;not null" json:"started_at"`
	FinishedAt   time.Time `gorm:"column:finished_at" json:"finished_at"`
	Status       string    `gorm:"column:status;size:32;index;not null" json:"status"`
	Trigger      string    `gorm:"column:trigger;size:32;not null" json:"trigger"`
	ArchiveName  string    `gorm:"column:archive_name;size:255" json:"archive_name"`
	DriveFileID  string    `gorm:"column:drive_file_id;size:128" json:"drive_file_id"`
	Bytes        int64     `gorm:"column:bytes" json:"bytes"`
	ErrorMessage string    `gorm:"column:error_message;type:text" json:"error_message"`
	CreatedAt    time.Time `gorm:"column:created_at" json:"created_at"`
}

type configStore struct {
	table  string
	onSave func(section string, data map[string]any)
}

func (s configStore) Load(section string) (map[string]any, error) {
	db, _, err := extension.OpenDB()
	if err != nil {
		return nil, err
	}
	if err := extension.MigrateConfigurationStore(db, s.table); err != nil {
		return nil, err
	}
	return extension.DBConfigurationStore(db, s.table).Load(section)
}

func (s configStore) Save(section string, data map[string]any) error {
	if section == configSection {
		freq := strings.TrimSpace(stringValue(data, "frequency"))
		if freq == "" {
			return fmt.Errorf("frequency is required")
		}
		if _, err := parseCron(freq); err != nil {
			return fmt.Errorf("invalid cron frequency: %w", err)
		}
	}
	db, _, err := extension.OpenDB()
	if err != nil {
		return err
	}
	if err := extension.MigrateConfigurationStore(db, s.table); err != nil {
		return err
	}
	if err := extension.DBConfigurationStore(db, s.table).Save(section, data); err != nil {
		return err
	}
	if s.onSave != nil {
		s.onSave(section, data)
	}
	return nil
}

func (app *backupApp) settingsStore() configStore {
	return configStore{table: app.settingsTable}
}

func (app *backupApp) loadSchedule() (scheduleSettings, error) {
	data, err := app.settingsStore().Load(configSection)
	if err != nil {
		return scheduleSettings{}, err
	}
	return scheduleSettings{
		Enabled:     boolValue(data, "enabled"),
		Frequency:   stringValue(data, "frequency"),
		RetainCount: intValue(data, "retain_count", 14),
	}, nil
}

func (app *backupApp) loadConnection() (connectionSettings, error) {
	data, err := app.settingsStore().Load(connectionSection)
	if err != nil {
		return connectionSettings{}, err
	}
	return connectionSettings{
		ClientID:     strings.TrimSpace(stringValue(data, "client_id")),
		ClientSecret: strings.TrimSpace(stringValue(data, "client_secret")),
		TokenJSON:    strings.TrimSpace(stringValue(data, "token_json")),
		FolderID:     strings.TrimSpace(stringValue(data, "folder_id")),
		FolderName:   strings.TrimSpace(stringValue(data, "folder_name")),
		AccountEmail: strings.TrimSpace(stringValue(data, "account_email")),
	}, nil
}

func (app *backupApp) saveConnection(conn connectionSettings) error {
	return app.settingsStore().Save(connectionSection, map[string]any{
		"client_id":     conn.ClientID,
		"client_secret": conn.ClientSecret,
		"token_json":    conn.TokenJSON,
		"folder_id":     conn.FolderID,
		"folder_name":   conn.FolderName,
		"account_email": conn.AccountEmail,
	})
}

func (conn connectionSettings) token() (*oauth2.Token, error) {
	if conn.TokenJSON == "" {
		return nil, fmt.Errorf("Google Drive is not connected")
	}
	var tok oauth2.Token
	if err := json.Unmarshal([]byte(conn.TokenJSON), &tok); err != nil {
		return nil, fmt.Errorf("decode oauth token: %w", err)
	}
	if tok.AccessToken == "" && tok.RefreshToken == "" {
		return nil, fmt.Errorf("Google Drive token is empty")
	}
	return &tok, nil
}

func (conn connectionSettings) connected() bool {
	return conn.ClientID != "" && conn.ClientSecret != "" && conn.TokenJSON != ""
}

func (conn connectionSettings) ready() bool {
	return conn.connected() && conn.FolderID != ""
}

func (app *backupApp) ensureLogsTable(db *gorm.DB) error {
	return db.Table(app.logsTable).AutoMigrate(&backupLog{})
}

func (app *backupApp) insertLog(db *gorm.DB, row *backupLog) error {
	if row.StartedAt.IsZero() {
		row.StartedAt = time.Now().UTC()
	}
	if row.CreatedAt.IsZero() {
		row.CreatedAt = time.Now().UTC()
	}
	return db.Table(app.logsTable).Create(row).Error
}

func (app *backupApp) updateLog(db *gorm.DB, row *backupLog) error {
	return db.Table(app.logsTable).Save(row).Error
}

type sitesFileConfig struct {
	DataRoot string `json:"data_root"`
}

func loadSitesFile() (dataRoot, configPath string, err error) {
	configPath = strings.TrimSpace(os.Getenv("CANNON_CONFIG"))
	if configPath == "" {
		return "", "", fmt.Errorf("CANNON_CONFIG is not set")
	}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return "", "", err
	}
	var cfg sitesFileConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return "", "", fmt.Errorf("parse sites.json: %w", err)
	}
	dataRoot = strings.TrimSpace(cfg.DataRoot)
	if dataRoot == "" {
		dataRoot = "./data"
	}
	if !filepath.IsAbs(dataRoot) {
		dataRoot = filepath.Join(filepath.Dir(configPath), dataRoot)
	}
	return absPath(dataRoot), configPath, nil
}

func stringValue(data map[string]any, key string) string {
	if data == nil {
		return ""
	}
	v, ok := data[key]
	if !ok || v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case fmt.Stringer:
		return t.String()
	default:
		return fmt.Sprint(t)
	}
}

func boolValue(data map[string]any, key string) bool {
	if data == nil {
		return false
	}
	v, ok := data[key]
	if !ok || v == nil {
		return false
	}
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return t == "1" || strings.EqualFold(t, "true") || strings.EqualFold(t, "on") || strings.EqualFold(t, "yes")
	case float64:
		return t != 0
	case int:
		return t != 0
	default:
		return false
	}
}

func intValue(data map[string]any, key string, fallback int) int {
	if data == nil {
		return fallback
	}
	v, ok := data[key]
	if !ok || v == nil {
		return fallback
	}
	switch t := v.(type) {
	case int:
		return t
	case int64:
		return int(t)
	case float64:
		return int(t)
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(t))
		if err != nil {
			return fallback
		}
		return n
	default:
		return fallback
	}
}
