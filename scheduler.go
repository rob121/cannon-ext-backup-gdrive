package main

import (
	"log"
	"strings"
	"sync"

	"github.com/robfig/cron/v3"
)

type backupScheduler struct {
	app  *backupApp
	mu   sync.Mutex
	cron *cron.Cron
}

func newBackupScheduler(app *backupApp) *backupScheduler {
	return &backupScheduler{app: app}
}

func parseCron(expr string) (cron.Schedule, error) {
	expr = strings.TrimSpace(expr)
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	return parser.Parse(expr)
}

func (s *backupScheduler) reload() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.cron != nil {
		s.cron.Stop()
		s.cron = nil
	}

	schedule, err := s.app.loadSchedule()
	if err != nil {
		log.Printf("backup-gdrive: load schedule: %v", err)
		return
	}
	if !schedule.Enabled {
		log.Printf("backup-gdrive: scheduled backups disabled")
		return
	}
	freq := strings.TrimSpace(schedule.Frequency)
	if freq == "" {
		freq = "0 2 * * *"
	}
	if _, err := parseCron(freq); err != nil {
		log.Printf("backup-gdrive: invalid cron %q: %v", freq, err)
		return
	}

	c := cron.New(cron.WithParser(cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)))
	_, err = c.AddFunc(freq, func() {
		log.Printf("backup-gdrive: starting scheduled backup")
		if _, err := s.app.runBackup("schedule"); err != nil {
			log.Printf("backup-gdrive: scheduled backup failed: %v", err)
		}
	})
	if err != nil {
		log.Printf("backup-gdrive: schedule add: %v", err)
		return
	}
	c.Start()
	s.cron = c
	log.Printf("backup-gdrive: scheduled backups enabled (%s)", freq)
}
