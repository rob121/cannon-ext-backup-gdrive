package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"
)

type driveFolder struct {
	ID   string
	Name string
}

func (app *backupApp) listDriveFolders(ctx context.Context, parentID string) ([]driveFolder, error) {
	svc, _, err := app.driveService(ctx)
	if err != nil {
		return nil, err
	}
	parentID = strings.TrimSpace(parentID)
	if parentID == "" {
		parentID = "root"
	}
	q := fmt.Sprintf("'%s' in parents and mimeType = 'application/vnd.google-apps.folder' and trashed = false", escapeDriveQuery(parentID))
	var out []driveFolder
	pageToken := ""
	for {
		call := svc.Files.List().
			Q(q).
			Spaces("drive").
			Fields("nextPageToken, files(id, name)").
			OrderBy("name").
			PageSize(100).
			SupportsAllDrives(true).
			IncludeItemsFromAllDrives(true)
		if pageToken != "" {
			call = call.PageToken(pageToken)
		}
		resp, err := call.Context(ctx).Do()
		if err != nil {
			return nil, err
		}
		for _, f := range resp.Files {
			out = append(out, driveFolder{ID: f.Id, Name: f.Name})
		}
		if resp.NextPageToken == "" {
			break
		}
		pageToken = resp.NextPageToken
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

func (app *backupApp) uploadBackupFile(ctx context.Context, localPath, folderID, fileName string) (fileID string, size int64, err error) {
	svc, _, err := app.driveService(ctx)
	if err != nil {
		return "", 0, err
	}
	f, err := os.Open(localPath)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", 0, err
	}
	meta := &drive.File{
		Name:    fileName,
		Parents: []string{folderID},
	}
	created, err := svc.Files.Create(meta).
		Media(f, googleapi.ContentType("application/gzip")).
		Fields("id, size").
		SupportsAllDrives(true).
		Context(ctx).
		Do()
	if err != nil {
		return "", 0, err
	}
	return created.Id, st.Size(), nil
}

func (app *backupApp) pruneOldBackups(ctx context.Context, folderID string, retain int) error {
	if retain <= 0 {
		return nil
	}
	svc, _, err := app.driveService(ctx)
	if err != nil {
		return err
	}
	q := fmt.Sprintf("'%s' in parents and name contains 'cannon-backup-' and trashed = false", escapeDriveQuery(folderID))
	type item struct {
		id      string
		created time.Time
	}
	var files []item
	pageToken := ""
	for {
		call := svc.Files.List().
			Q(q).
			Spaces("drive").
			Fields("nextPageToken, files(id, name, createdTime)").
			PageSize(100).
			SupportsAllDrives(true).
			IncludeItemsFromAllDrives(true)
		if pageToken != "" {
			call = call.PageToken(pageToken)
		}
		resp, err := call.Context(ctx).Do()
		if err != nil {
			return err
		}
		for _, f := range resp.Files {
			if !strings.HasPrefix(f.Name, "cannon-backup-") {
				continue
			}
			t, _ := time.Parse(time.RFC3339, f.CreatedTime)
			files = append(files, item{id: f.Id, created: t})
		}
		if resp.NextPageToken == "" {
			break
		}
		pageToken = resp.NextPageToken
	}
	if len(files) <= retain {
		return nil
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].created.After(files[j].created)
	})
	for _, f := range files[retain:] {
		if err := svc.Files.Delete(f.id).SupportsAllDrives(true).Context(ctx).Do(); err != nil {
			return err
		}
	}
	return nil
}

func escapeDriveQuery(s string) string {
	return strings.ReplaceAll(s, `'`, `\'`)
}

func absPath(p string) string {
	if p == "" {
		return p
	}
	if filepath.IsAbs(p) {
		return p
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return abs
}
