package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rob121/cannon/extension"
)

func (app *backupApp) runBackup(trigger string) (*backupLog, error) {
	app.mu.Lock()
	if app.running {
		app.mu.Unlock()
		return nil, fmt.Errorf("a backup is already running")
	}
	app.running = true
	app.mu.Unlock()
	defer func() {
		app.mu.Lock()
		app.running = false
		app.mu.Unlock()
	}()

	db, _, err := extension.OpenDB()
	if err != nil {
		return nil, err
	}
	if err := app.ensureLogsTable(db); err != nil {
		return nil, err
	}

	row := &backupLog{
		StartedAt: time.Now().UTC(),
		Status:    "running",
		Trigger:   trigger,
		CreatedAt: time.Now().UTC(),
	}
	if err := app.insertLog(db, row); err != nil {
		return nil, err
	}

	fail := func(msg string) (*backupLog, error) {
		row.Status = "failed"
		row.FinishedAt = time.Now().UTC()
		row.ErrorMessage = msg
		_ = app.updateLog(db, row)
		return row, fmt.Errorf("%s", msg)
	}

	conn, err := app.loadConnection()
	if err != nil {
		return fail(err.Error())
	}
	if !conn.ready() {
		return fail("Google Drive is not fully configured (connect account and select a folder)")
	}

	dataRoot, configPath, err := loadSitesFile()
	if err != nil {
		return fail("load sites.json: " + err.Error())
	}
	if st, err := os.Stat(dataRoot); err != nil || !st.IsDir() {
		return fail(fmt.Sprintf("data_root %q is not a directory", dataRoot))
	}

	tmpDir, err := os.MkdirTemp("", "cannon-backup-*")
	if err != nil {
		return fail(err.Error())
	}
	defer os.RemoveAll(tmpDir)

	archiveName := fmt.Sprintf("cannon-backup-%s.tar.gz", time.Now().UTC().Format("20060102-150405"))
	archivePath := filepath.Join(tmpDir, archiveName)
	if err := createBackupArchive(archivePath, dataRoot, configPath); err != nil {
		return fail("create archive: " + err.Error())
	}
	st, err := os.Stat(archivePath)
	if err != nil {
		return fail(err.Error())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()

	fileID, size, err := app.uploadBackupFile(ctx, archivePath, conn.FolderID, archiveName)
	if err != nil {
		return fail("upload to Google Drive: " + err.Error())
	}
	row.ArchiveName = archiveName
	row.DriveFileID = fileID
	row.Bytes = size
	if row.Bytes == 0 {
		row.Bytes = st.Size()
	}

	schedule, _ := app.loadSchedule()
	if schedule.RetainCount > 0 {
		if err := app.pruneOldBackups(ctx, conn.FolderID, schedule.RetainCount); err != nil {
			log.Printf("backup-gdrive: prune old backups: %v", err)
		}
	}

	row.Status = "success"
	row.FinishedAt = time.Now().UTC()
	row.ErrorMessage = ""
	if err := app.updateLog(db, row); err != nil {
		return row, err
	}
	return row, nil
}

func createBackupArchive(archivePath, dataRoot, sitesJSONPath string) error {
	out, err := os.Create(archivePath)
	if err != nil {
		return err
	}
	defer out.Close()

	gz := gzip.NewWriter(out)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()

	// Always include sites.json at archive root.
	if err := addFileToTar(tw, sitesJSONPath, "sites.json"); err != nil {
		return fmt.Errorf("add sites.json: %w", err)
	}

	skipNames := map[string]bool{
		"sockets": true, // runtime unix sockets
	}
	return filepath.Walk(dataRoot, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(dataRoot, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		// Skip sockets dir and anything under it.
		parts := strings.Split(rel, string(os.PathSeparator))
		if len(parts) > 0 && skipNames[parts[0]] {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		// Skip unix sockets and our own temp archives if nested.
		if info.Mode()&os.ModeSocket != 0 {
			return nil
		}
		if strings.HasSuffix(info.Name(), ".sock") {
			return nil
		}
		name := filepath.ToSlash(filepath.Join("data_root", rel))
		if info.IsDir() {
			hdr, err := tar.FileInfoHeader(info, "")
			if err != nil {
				return err
			}
			hdr.Name = name + "/"
			return tw.WriteHeader(hdr)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		return addFileToTar(tw, path, name)
	})
}

func addFileToTar(tw *tar.Writer, srcPath, name string) error {
	f, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	hdr, err := tar.FileInfoHeader(st, "")
	if err != nil {
		return err
	}
	hdr.Name = name
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err = io.Copy(tw, f)
	return err
}
