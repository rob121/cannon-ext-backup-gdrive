package main

import (
	"embed"
	"encoding/json"
	"log"
	"os"
	"sync"

	"github.com/rob121/cannon/extension"
)

//go:embed all:templates
var templateFiles embed.FS

//go:embed help/*.md
var helpFiles embed.FS

const (
	extensionName        = "cannon-ext-backup-gdrive"
	extensionTitle       = "Google Drive Backup"
	extensionDescription = "Backs up Cannon data_root and sites.json to Google Drive on a schedule or on demand."
	extensionVersion     = "0.1.1"
	updateURLBase        = "https://github.com/rob121/cannon-ext-backup-gdrive/releases/download"
	defaultAdminMenu     = "Drive Backup"
	configSection        = "schedule"
	connectionSection    = "connection"
)

var scheduleConfigSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "enabled": {
      "type": "boolean",
      "title": "Enable Scheduled Backups",
      "default": false,
      "description": "When enabled, backups run automatically using the cron expression below."
    },
    "frequency": {
      "type": "string",
      "title": "Frequency (cron)",
      "default": "0 2 * * *",
      "description": "Standard 5-field cron expression (minute hour day-of-month month day-of-week). Example: 0 2 * * * runs daily at 02:00."
    },
    "retain_count": {
      "type": "integer",
      "title": "Retain Count",
      "minimum": 0,
      "default": 14,
      "description": "Keep this many recent backup files in Google Drive (0 = keep all)."
    }
  },
  "required": ["frequency"]
}`)

var scheduleConfigUI = json.RawMessage(`{
  "type": "VerticalLayout",
  "elements": [
    {"type": "Control", "scope": "#/properties/enabled"},
    {"type": "Control", "scope": "#/properties/frequency"},
    {"type": "Control", "scope": "#/properties/retain_count"}
  ]
}`)

type backupApp struct {
	info          extension.Info
	templates     *extension.Templates
	settingsTable string
	logsTable     string

	mu         sync.Mutex
	running    bool
	oauthState sync.Map // state -> expiry unix
	scheduler  *backupScheduler

	schemaOnce sync.Once
	schemaErr  error
}

func main() {
	app, s := newServer()
	if _, dataQuery := extension.DataArgKey(os.Args); !dataQuery {
		app.startScheduler()
	}
	if err := s.Run(); err != nil {
		log.Fatal(err)
	}
}

func newServer() (*backupApp, *extension.Server) {
	info := extension.Info{
		Name:          extensionName,
		Title:         extensionTitle,
		Description:   extensionDescription,
		Version:       extensionVersion,
		UpdateURLBase: updateURLBase,
		AdminMenuName: defaultAdminMenu,
	}
	app := &backupApp{
		info:          info,
		templates:     extension.NewTemplates(templateFiles, "templates"),
		settingsTable: extension.TableName(info.TablePrefix(), "settings"),
		logsTable:     extension.TableName(info.TablePrefix(), "logs"),
	}
	app.scheduler = newBackupScheduler(app)

	s := extension.New(info)
	s.EmbedTemplates(templateFiles, "templates")
	s.EmbedHelp(helpFiles, "help")
	s.OnInstall(app.install)
	s.RegisterPermissions([]extension.PermissionDef{
		{
			ID:          "backup.view",
			DisplayName: "View Drive backups",
			Description: "View backup history and status.",
			Category:    "backup",
		},
		{
			ID:          "backup.run",
			DisplayName: "Run Drive backups",
			Description: "Trigger on-demand backups.",
			Category:    "backup",
		},
		{
			ID:          "backup.setup",
			DisplayName: "Configure Drive backup",
			Description: "Connect Google Drive and choose a destination folder.",
			Category:    "backup",
			Dangerous:   true,
		},
	})
	s.OnConfiguration(extension.MapConfiguration([]extension.ConfigurationDefinition{
		{
			ID:       configSection,
			Title:    "Backup Schedule",
			Schema:   scheduleConfigSchema,
			UISchema: scheduleConfigUI,
		},
	}, configStore{table: app.settingsTable, onSave: app.onScheduleSaved}))
	s.HandleAdmin("/admin/", app.admin)

	return app, s
}

func (app *backupApp) install(req extension.WireRequest) error {
	db, _, err := extension.OpenDB()
	if err != nil {
		return err
	}
	return app.ensureSchema(db)
}

func (app *backupApp) onScheduleSaved(section string, data map[string]any) {
	if section != configSection {
		return
	}
	app.scheduler.reload()
}

func (app *backupApp) startScheduler() {
	app.scheduler.reload()
}
