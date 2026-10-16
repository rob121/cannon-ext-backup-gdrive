package main

import (
	"context"
	"fmt"
	htmlpkg "html"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/rob121/cannon/extension"
)

func (app *backupApp) admin(req extension.WireRequest) extension.WireResponse {
	adminURL, err := parseWireURL(req.URL)
	if err != nil {
		return extension.Error(http.StatusBadRequest, err.Error())
	}
	basePath, parts := adminRoute(adminURL.Path)
	if basePath == "" {
		basePath = adminLogURL()
	}

	// Fixed OAuth callback — must work even if the admin session check is flaky after Google redirect.
	if len(parts) == 2 && parts[0] == "oauth" && parts[1] == "callback" {
		return app.oauthCallback(req)
	}

	if !extension.UserAuthenticated(req) {
		return extension.Error(http.StatusForbidden, "sign in required")
	}
	db, _, err := extension.OpenDB()
	if err != nil {
		return extension.Error(http.StatusInternalServerError, err.Error())
	}
	if err := app.ensureLogsTable(db); err != nil {
		return extension.Error(http.StatusInternalServerError, err.Error())
	}
	if err := extension.MigrateConfigurationStore(db, app.settingsTable); err != nil {
		return extension.Error(http.StatusInternalServerError, err.Error())
	}

	csrf := csrfField(req)

	switch {
	case len(parts) == 0:
		return app.adminBackups(req, adminURL, basePath, csrf)
	case len(parts) == 1 && parts[0] == "backup-now":
		if !strings.EqualFold(req.Method, http.MethodPost) {
			return extension.Error(http.StatusMethodNotAllowed, "method not allowed")
		}
		_, runErr := app.runBackup("manual")
		loc := basePath
		if runErr != nil {
			loc += "?error=" + url.QueryEscape(runErr.Error())
		} else {
			loc += "?notice=" + url.QueryEscape("Backup completed successfully.")
		}
		return extension.Redirect(http.StatusSeeOther, loc)
	case len(parts) == 1 && parts[0] == "setup":
		return app.adminSetup(req, adminURL, basePath, csrf)
	case len(parts) == 2 && parts[0] == "setup" && parts[1] == "credentials":
		if !strings.EqualFold(req.Method, http.MethodPost) {
			return extension.Error(http.StatusMethodNotAllowed, "method not allowed")
		}
		return app.saveCredentials(req, basePath)
	case len(parts) == 2 && parts[0] == "setup" && parts[1] == "connect":
		// GET or POST — external Google redirect must not go through Turbo Drive.
		conn, err := app.loadConnection()
		if err != nil {
			return extension.Redirect(http.StatusSeeOther, basePath+"/setup?error="+url.QueryEscape(err.Error()))
		}
		authURL, err := app.startOAuthURL(conn, req)
		if err != nil {
			return extension.Redirect(http.StatusSeeOther, basePath+"/setup?error="+url.QueryEscape(err.Error()))
		}
		return extension.Redirect(http.StatusSeeOther, authURL)
	case len(parts) == 2 && parts[0] == "setup" && parts[1] == "disconnect":
		if !strings.EqualFold(req.Method, http.MethodPost) {
			return extension.Error(http.StatusMethodNotAllowed, "method not allowed")
		}
		conn, err := app.loadConnection()
		if err != nil {
			return extension.Redirect(http.StatusSeeOther, basePath+"/setup?error="+url.QueryEscape(err.Error()))
		}
		conn.TokenJSON = ""
		conn.AccountEmail = ""
		conn.FolderID = ""
		conn.FolderName = ""
		if err := app.saveConnection(conn); err != nil {
			return extension.Redirect(http.StatusSeeOther, basePath+"/setup?error="+url.QueryEscape(err.Error()))
		}
		return extension.Redirect(http.StatusSeeOther, basePath+"/setup?notice="+url.QueryEscape("Disconnected from Google Drive."))
	case len(parts) == 2 && parts[0] == "setup" && parts[1] == "folder":
		if !strings.EqualFold(req.Method, http.MethodPost) {
			return extension.Error(http.StatusMethodNotAllowed, "method not allowed")
		}
		return app.saveFolder(req, basePath)
	default:
		return extension.Error(http.StatusNotFound, "admin route not found")
	}
}

func (app *backupApp) adminBackups(req extension.WireRequest, adminURL *url.URL, basePath string, csrf template.HTML) extension.WireResponse {
	db, _, err := extension.OpenDB()
	if err != nil {
		return extension.Error(http.StatusInternalServerError, err.Error())
	}
	page := parsePage(adminURL)
	const pageSize = 25
	var total int64
	_ = db.Table(app.logsTable).Count(&total).Error
	var rows []backupLog
	err = db.Table(app.logsTable).
		Order("started_at desc, log_id desc").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Find(&rows).Error
	if err != nil {
		return extension.Error(http.StatusInternalServerError, err.Error())
	}
	conn, _ := app.loadConnection()
	schedule, _ := app.loadSchedule()
	notice := adminURL.Query().Get("notice")
	errMsg := adminURL.Query().Get("error")
	status := buildBackupStatus(conn, schedule, rows, total, basePath)

	return app.render("backup/admin_log.html", map[string]any{
		"Title":       "Drive Backups",
		"Subtitle":    "Backup history for data_root and sites.json.",
		"BasePath":    basePath,
		"SetupURL":    basePath + "/setup",
		"SettingsURL": "/admin/configuration/extensions/" + url.PathEscape(extensionName),
		"Rows":        rows,
		"Page":        page,
		"PageSize":    pageSize,
		"PageCount":   pageCount(total, pageSize),
		"Total":       total,
		"Notice":      notice,
		"Error":       errMsg,
		"CSRFField":   csrf,
		"Running":     app.isRunning(),
		"Status":      status,
	})
}

type backupStatusView struct {
	Ready            bool
	HasCredentials   bool
	Connected        bool
	HasFolder        bool
	ScheduleOn       bool
	Frequency        string
	RetainCount      int
	FolderName       string
	AccountEmail     string
	Tone             string // success | warning | muted
	Headline         string
	Detail           string
	NextLabel        string
	NextURL          string
	LastStatus       string
	LastStartedAt    time.Time
	LastArchiveName  string
	CompletedSteps   int
	TotalSteps       int
}

func buildBackupStatus(conn connectionSettings, schedule scheduleSettings, rows []backupLog, total int64, basePath string) backupStatusView {
	st := backupStatusView{
		HasCredentials: conn.ClientID != "" && conn.ClientSecret != "",
		Connected:      conn.connected(),
		HasFolder:      conn.FolderID != "",
		Ready:          conn.ready(),
		ScheduleOn:     schedule.Enabled,
		Frequency:      strings.TrimSpace(schedule.Frequency),
		RetainCount:    schedule.RetainCount,
		FolderName:     conn.FolderName,
		AccountEmail:   conn.AccountEmail,
		TotalSteps:     3,
	}
	if st.HasCredentials {
		st.CompletedSteps++
	}
	if st.Connected {
		st.CompletedSteps++
	}
	if st.HasFolder {
		st.CompletedSteps++
	}
	if st.Frequency == "" {
		st.Frequency = "0 2 * * *"
	}
	if len(rows) > 0 {
		st.LastStatus = rows[0].Status
		st.LastStartedAt = rows[0].StartedAt
		st.LastArchiveName = rows[0].ArchiveName
	}
	_ = total

	switch {
	case !st.HasCredentials:
		st.Tone = "muted"
		st.Headline = "Setup required"
		st.Detail = "Add Google OAuth credentials to start connecting Drive."
		st.NextLabel = "Start setup"
		st.NextURL = basePath + "/setup"
	case !st.Connected:
		st.Tone = "warning"
		st.Headline = "Connect Google Drive"
		st.Detail = "Credentials are saved. Authorize access so backups can upload."
		st.NextLabel = "Connect Drive"
		st.NextURL = basePath + "/setup"
	case !st.HasFolder:
		st.Tone = "warning"
		st.Headline = "Choose a backup folder"
		st.Detail = "Drive is connected. Pick the destination folder for archives."
		st.NextLabel = "Choose folder"
		st.NextURL = basePath + "/setup"
	default:
		st.Tone = "success"
		st.Headline = "Ready to back up"
		st.Detail = "Archives include data_root and sites.json."
		if st.ScheduleOn {
			st.Detail = fmt.Sprintf("Scheduled backups are on (%s).", st.Frequency)
		} else {
			st.Detail = "Manual backups are available. Enable a schedule anytime in Schedule settings."
		}
		st.NextLabel = ""
		st.NextURL = ""
	}
	return st
}

func (app *backupApp) adminSetup(req extension.WireRequest, adminURL *url.URL, basePath string, csrf template.HTML) extension.WireResponse {
	conn, err := app.loadConnection()
	if err != nil {
		return extension.Error(http.StatusInternalServerError, err.Error())
	}
	parentID := strings.TrimSpace(adminURL.Query().Get("parent"))
	var folders []driveFolder
	var folderErr string
	if conn.connected() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		folders, err = app.listDriveFolders(ctx, parentID)
		if err != nil {
			folderErr = err.Error()
		}
	}
	redirectURL, _ := app.oauthRedirectURL(req)
	notice := adminURL.Query().Get("notice")
	if adminURL.Query().Get("oauth") == "connected" {
		notice = "Google Drive connected. Select a destination folder below."
	}
	errMsg := adminURL.Query().Get("error")
	if oauthErr := adminURL.Query().Get("oauth_error"); oauthErr != "" {
		errMsg = oauthErr
	}
	parentLabel := "My Drive"
	if parentID != "" && parentID != "root" {
		parentLabel = parentID
	}
	hasCredentials := conn.ClientID != "" && conn.ClientSecret != ""
	connected := conn.connected()
	ready := conn.ready()
	completed := 0
	if hasCredentials {
		completed++
	}
	if connected {
		completed++
	}
	if conn.FolderID != "" {
		completed++
	}

	return app.render("backup/admin_setup.html", map[string]any{
		"Title":          "Drive Backup Setup",
		"Subtitle":       "Connect Google Drive and choose a backup folder.",
		"BasePath":       basePath,
		"LogURL":         basePath,
		"SettingsURL":    "/admin/configuration/extensions/" + url.PathEscape(extensionName),
		"CSRFField":      csrf,
		"Conn":           conn,
		"HasCredentials": hasCredentials,
		"Connected":      connected,
		"Ready":          ready,
		"CompletedSteps": completed,
		"Folders":        folders,
		"FolderError":    folderErr,
		"ParentID":       parentID,
		"ParentLabel":    parentLabel,
		"RedirectURL":    redirectURL,
		"Notice":         notice,
		"Error":          errMsg,
		"DocsClientURL":  "https://rclone.org/drive/#making-your-own-client-id",
		"DocsDriveURL":   "https://rclone.org/drive/",
	})
}

func (app *backupApp) saveCredentials(req extension.WireRequest, basePath string) extension.WireResponse {
	values, err := url.ParseQuery(req.Body)
	if err != nil {
		return extension.Redirect(http.StatusSeeOther, basePath+"/setup?error="+url.QueryEscape("invalid form"))
	}
	conn, err := app.loadConnection()
	if err != nil {
		return extension.Redirect(http.StatusSeeOther, basePath+"/setup?error="+url.QueryEscape(err.Error()))
	}
	prevID := conn.ClientID
	conn.ClientID = strings.TrimSpace(values.Get("client_id"))
	secret := strings.TrimSpace(values.Get("client_secret"))
	if secret != "" {
		conn.ClientSecret = secret
	}
	if conn.ClientID == "" {
		return extension.Redirect(http.StatusSeeOther, basePath+"/setup?error="+url.QueryEscape("client ID is required"))
	}
	if conn.ClientSecret == "" {
		return extension.Redirect(http.StatusSeeOther, basePath+"/setup?error="+url.QueryEscape("client secret is required"))
	}
	if prevID != "" && prevID != conn.ClientID {
		conn.TokenJSON = ""
		conn.AccountEmail = ""
		conn.FolderID = ""
		conn.FolderName = ""
	}
	if err := app.saveConnection(conn); err != nil {
		return extension.Redirect(http.StatusSeeOther, basePath+"/setup?error="+url.QueryEscape(err.Error()))
	}
	return extension.Redirect(http.StatusSeeOther, basePath+"/setup?notice="+url.QueryEscape("OAuth credentials saved."))
}

func (app *backupApp) saveFolder(req extension.WireRequest, basePath string) extension.WireResponse {
	values, err := url.ParseQuery(req.Body)
	if err != nil {
		return extension.Redirect(http.StatusSeeOther, basePath+"/setup?error="+url.QueryEscape("invalid form"))
	}
	folderID := strings.TrimSpace(values.Get("folder_id"))
	folderName := strings.TrimSpace(values.Get("folder_name"))
	if folderID == "" {
		return extension.Redirect(http.StatusSeeOther, basePath+"/setup?error="+url.QueryEscape("folder is required"))
	}
	if folderName == "" {
		folderName = folderID
	}
	conn, err := app.loadConnection()
	if err != nil {
		return extension.Redirect(http.StatusSeeOther, basePath+"/setup?error="+url.QueryEscape(err.Error()))
	}
	conn.FolderID = folderID
	conn.FolderName = folderName
	if err := app.saveConnection(conn); err != nil {
		return extension.Redirect(http.StatusSeeOther, basePath+"/setup?error="+url.QueryEscape(err.Error()))
	}
	return extension.Redirect(http.StatusSeeOther, basePath+"/setup?notice="+url.QueryEscape("Backup folder saved: "+folderName))
}

func (app *backupApp) isRunning() bool {
	app.mu.Lock()
	defer app.mu.Unlock()
	return app.running
}

func (app *backupApp) render(name string, data map[string]any) extension.WireResponse {
	body, err := app.templates.WithFuncs(template.FuncMap{
		"add":         func(a, b int) int { return a + b },
		"sub":         func(a, b int) int { return a - b },
		"pageCount":   pageCount,
		"formatBytes": formatBytes,
		"formatTime": func(t time.Time) string {
			if t.IsZero() {
				return "—"
			}
			return t.Local().Format("2006-01-02 15:04:05")
		},
		"statusBadge": statusBadge,
	}).Execute(name, data)
	if err != nil {
		return extension.Error(http.StatusInternalServerError, err.Error())
	}
	return extension.HTML(http.StatusOK, body)
}

func csrfField(req extension.WireRequest) template.HTML {
	return template.HTML(extension.CSRFHiddenField(req))
}

func parseWireURL(raw string) (*url.URL, error) {
	if strings.TrimSpace(raw) == "" {
		return url.Parse(adminLogURL())
	}
	return url.Parse(raw)
}

func adminRoute(path string) (base string, parts []string) {
	path = strings.TrimSpace(path)
	marker := "/extension-apps/" + extensionName
	idx := strings.Index(path, marker)
	if idx < 0 {
		marker = "/extension-apps/" + url.PathEscape(extensionName)
		idx = strings.Index(path, marker)
	}
	if idx < 0 {
		return "", nil
	}
	base = path[:idx+len(marker)]
	rest := strings.TrimPrefix(path[idx+len(marker):], "/")
	if rest == "" {
		return base, nil
	}
	return base, strings.Split(rest, "/")
}

func parsePage(u *url.URL) int {
	page, _ := strconv.Atoi(u.Query().Get("page"))
	if page < 1 {
		return 1
	}
	return page
}

func pageCount(total int64, pageSize int) int {
	if pageSize <= 0 {
		return 1
	}
	n := int((total + int64(pageSize) - 1) / int64(pageSize))
	if n < 1 {
		return 1
	}
	return n
}

func formatBytes(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"KB", "MB", "GB", "TB"}
	v := float64(n)
	for _, u := range units {
		v /= 1024
		if v < 1024 {
			return fmt.Sprintf("%.1f %s", v, u)
		}
	}
	return fmt.Sprintf("%.1f PB", v/1024)
}

func statusBadge(status string) template.HTML {
	status = strings.ToLower(strings.TrimSpace(status))
	cls := "admin-badge-inactive"
	label := htmlpkg.EscapeString(status)
	switch status {
	case "success":
		cls = "admin-badge-active"
		label = "Success"
	case "failed":
		cls = "admin-badge-inactive"
		label = "Failed"
	case "running":
		cls = "admin-badge-active"
		label = "Running"
	}
	return template.HTML(fmt.Sprintf(`<span class="admin-badge %s"><span class="admin-badge-dot"></span>%s</span>`, cls, label))
}
