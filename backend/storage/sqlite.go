package storage

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"m3u8-downloader-web/model"

	_ "modernc.org/sqlite"
)

type SQLiteStorage struct {
	db *sql.DB
}

func NewSQLiteStorage() (*SQLiteStorage, error) {
	log.Println("[SQLite] Initializing database...")
	
	pwd, err := os.Getwd()
	if err != nil {
		log.Printf("[SQLite] Error getting working directory: %v\n", err)
		return nil, err
	}

	dbPath := filepath.Join(pwd, "downloader.db")
	log.Printf("[SQLite] Database path: %s\n", dbPath)
	
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		log.Printf("[SQLite] Error opening database: %v\n", err)
		return nil, err
	}

	db.SetMaxOpenConns(1)
	
	if err := db.Ping(); err != nil {
		log.Printf("[SQLite] Error pinging database: %v\n", err)
		return nil, err
	}

	err = createTables(db)
	if err != nil {
		log.Printf("[SQLite] Error creating tables: %v\n", err)
		return nil, err
	}

	log.Printf("[SQLite] Database initialized: %s\n", dbPath)

	return &SQLiteStorage{db: db}, nil
}

func createTables(db *sql.DB) error {
	tasksTable := `
	CREATE TABLE IF NOT EXISTS tasks (
		id TEXT PRIMARY KEY,
		url TEXT NOT NULL,
		name TEXT NOT NULL,
		status TEXT NOT NULL,
		progress REAL DEFAULT 0,
		speed TEXT DEFAULT '0 KB/s',
		thread_count INTEGER DEFAULT 10,
		host_type TEXT DEFAULT 'v1',
		cookie TEXT,
		referer TEXT,
		auto_clear BOOLEAN DEFAULT 1,
		save_path TEXT,
		output_path TEXT,
		enable_webdav BOOLEAN DEFAULT 0,
		webdav_url TEXT DEFAULT '',
		webdav_username TEXT DEFAULT '',
		webdav_password TEXT DEFAULT '',
		webdav_remote_dir TEXT DEFAULT '',
		delete_after_upload BOOLEAN DEFAULT 0,
		total_segments INTEGER DEFAULT 0,
		downloaded_segments INTEGER DEFAULT 0,
		created_at DATETIME NOT NULL,
		completed_at DATETIME
	);
	`

	_, err := db.Exec(tasksTable)
	if err != nil {
		return err
	}

	logsTable := `
	CREATE TABLE IF NOT EXISTS logs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		task_id TEXT NOT NULL,
		level TEXT NOT NULL,
		message TEXT NOT NULL,
		timestamp DATETIME NOT NULL,
		FOREIGN KEY(task_id) REFERENCES tasks(id)
	);
	`

	_, err = db.Exec(logsTable)
	if err != nil {
		return err
	}

	migrateTasksTable(db)
	migrateSettingsTable(db)

	settingsTable := `
	CREATE TABLE IF NOT EXISTS settings (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		default_thread_count INTEGER DEFAULT 24,
		default_output_name TEXT DEFAULT 'movie',
		default_save_path TEXT DEFAULT '',
		auto_clear BOOLEAN DEFAULT 1,
		host_type TEXT DEFAULT 'v1',
		enable_webdav BOOLEAN DEFAULT 0,
		webdav_url TEXT DEFAULT '',
		webdav_username TEXT DEFAULT '',
		webdav_password TEXT DEFAULT '',
		webdav_remote_dir TEXT DEFAULT '',
		delete_after_upload BOOLEAN DEFAULT 0,
		task_sort_order TEXT DEFAULT 'desc',
		default_referer TEXT DEFAULT ''
	);
	`

	_, err = db.Exec(settingsTable)
	if err != nil {
		return err
	}

	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM settings WHERE id = 1").Scan(&count)
	if err == nil && count == 0 {
		_, _ = db.Exec("INSERT INTO settings (id) VALUES (1)")
	}

	return nil
}

func migrateTasksTable(db *sql.DB) {
	columns := map[string]string{
		"thread_count":        "INTEGER DEFAULT 24",
		"host_type":           "TEXT DEFAULT 'v1'",
		"cookie":              "TEXT DEFAULT ''",
		"referer":             "TEXT DEFAULT ''",
		"auto_clear":          "BOOLEAN DEFAULT 1",
		"save_path":           "TEXT DEFAULT ''",
		"enable_webdav":       "BOOLEAN DEFAULT 0",
		"webdav_url":          "TEXT DEFAULT ''",
		"webdav_username":     "TEXT DEFAULT ''",
		"webdav_password":     "TEXT DEFAULT ''",
		"webdav_remote_dir":    "TEXT DEFAULT ''",
		"delete_after_upload": "BOOLEAN DEFAULT 0",
	}

	for col, colType := range columns {
		query := fmt.Sprintf("ALTER TABLE tasks ADD COLUMN %s %s", col, colType)
		_, err := db.Exec(query)
		if err != nil {
			continue
		}
		log.Printf("[SQLite] Added column %s to tasks table\n", col)
	}
}

func migrateSettingsTable(db *sql.DB) {
	columns := map[string]string{
		"task_sort_order": "TEXT DEFAULT 'desc'",
		"default_referer": "TEXT DEFAULT ''",
	}

	for col, colType := range columns {
		query := fmt.Sprintf("ALTER TABLE settings ADD COLUMN %s %s", col, colType)
		_, err := db.Exec(query)
		if err != nil {
			continue
		}
		log.Printf("[SQLite] Added column %s to settings table\n", col)
	}
}

func (s *SQLiteStorage) GetSettings() (*model.Settings, error) {
	query := `
	SELECT 
		default_thread_count, default_output_name, default_save_path,
		auto_clear, host_type, enable_webdav, webdav_url,
		webdav_username, webdav_password, webdav_remote_dir,
		delete_after_upload, task_sort_order, default_referer
	FROM settings WHERE id = 1
	`

	settings := &model.Settings{}
	err := s.db.QueryRow(query).Scan(
		&settings.DefaultThreadCount,
		&settings.DefaultOutputName,
		&settings.DefaultSavePath,
		&settings.AutoClear,
		&settings.HostType,
		&settings.EnableWebDAV,
		&settings.WebDAVURL,
		&settings.WebDAVUsername,
		&settings.WebDAVPassword,
		&settings.WebDAVRemoteDir,
		&settings.DeleteAfterUpload,
		&settings.TaskSortOrder,
		&settings.DefaultReferer,
	)

	if err == sql.ErrNoRows {
		return nil, nil
	}

	return settings, err
}

func (s *SQLiteStorage) SaveSettings(settings *model.Settings) error {
	query := `
	UPDATE settings SET
		default_thread_count = ?,
		default_output_name = ?,
		default_save_path = ?,
		auto_clear = ?,
		host_type = ?,
		enable_webdav = ?,
		webdav_url = ?,
		webdav_username = ?,
		webdav_password = ?,
		webdav_remote_dir = ?,
		delete_after_upload = ?,
		task_sort_order = ?,
		default_referer = ?
	WHERE id = 1
	`

	_, err := s.db.Exec(
		query,
		settings.DefaultThreadCount,
		settings.DefaultOutputName,
		settings.DefaultSavePath,
		settings.AutoClear,
		settings.HostType,
		settings.EnableWebDAV,
		settings.WebDAVURL,
		settings.WebDAVUsername,
		settings.WebDAVPassword,
		settings.WebDAVRemoteDir,
		settings.DeleteAfterUpload,
		settings.TaskSortOrder,
		settings.DefaultReferer,
	)

	return err
}

func (s *SQLiteStorage) AddTask(task *model.Task) error {
	query := `
	INSERT INTO tasks (
		id, url, name, status, progress, speed, 
		thread_count, host_type, cookie, referer, auto_clear, save_path,
		enable_webdav, webdav_url, webdav_username, webdav_password,
		webdav_remote_dir, delete_after_upload,
		total_segments, downloaded_segments, created_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

	_, err := s.db.Exec(
		query,
		task.ID,
		task.URL,
		task.Name,
		task.Status,
		task.Progress,
		task.Speed,
		task.ThreadCount,
		task.HostType,
		task.Cookie,
		task.Referer,
		task.AutoClear,
		task.SavePath,
		task.EnableWebDAV,
		task.WebDAVURL,
		task.WebDAVUsername,
		task.WebDAVPassword,
		task.WebDAVRemoteDir,
		task.DeleteAfterUpload,
		task.TotalSegments,
		task.DownloadedSegments,
		task.CreatedAt.Format(time.RFC3339),
	)

	return err
}

func (s *SQLiteStorage) UpdateTask(task *model.Task) error {
	query := `
	UPDATE tasks SET
		status = ?,
		progress = ?,
		speed = ?,
		output_path = ?,
		total_segments = ?,
		downloaded_segments = ?,
		completed_at = ?,
		enable_webdav = ?,
		webdav_url = ?,
		webdav_username = ?,
		webdav_password = ?,
		webdav_remote_dir = ?,
		delete_after_upload = ?
	WHERE id = ?
	`

	completedAt := ""
	if task.CompletedAt != nil {
		completedAt = task.CompletedAt.Format(time.RFC3339)
	}

	_, err := s.db.Exec(
		query,
		task.Status,
		task.Progress,
		task.Speed,
		task.OutputPath,
		task.TotalSegments,
		task.DownloadedSegments,
		completedAt,
		task.EnableWebDAV,
		task.WebDAVURL,
		task.WebDAVUsername,
		task.WebDAVPassword,
		task.WebDAVRemoteDir,
		task.DeleteAfterUpload,
		task.ID,
	)

	return err
}

func (s *SQLiteStorage) GetTask(taskID string) (*model.Task, error) {
	query := `
	SELECT 
		id, url, name, status, progress, speed,
		thread_count, host_type, cookie, referer, auto_clear, save_path,
		output_path, enable_webdav, webdav_url, webdav_username,
		webdav_password, webdav_remote_dir, delete_after_upload,
		total_segments, downloaded_segments,
		created_at, completed_at
	FROM tasks WHERE id = ?
	`

	row := s.db.QueryRow(query, taskID)

	task := &model.Task{}
	var completedAtStr string

	err := row.Scan(
		&task.ID,
		&task.URL,
		&task.Name,
		&task.Status,
		&task.Progress,
		&task.Speed,
		&task.ThreadCount,
		&task.HostType,
		&task.Cookie,
		&task.Referer,
		&task.AutoClear,
		&task.SavePath,
		&task.OutputPath,
		&task.EnableWebDAV,
		&task.WebDAVURL,
		&task.WebDAVUsername,
		&task.WebDAVPassword,
		&task.WebDAVRemoteDir,
		&task.DeleteAfterUpload,
		&task.TotalSegments,
		&task.DownloadedSegments,
		&task.CreatedAt,
		&completedAtStr,
	)

	if err == sql.ErrNoRows {
		return nil, nil
	}

	if err != nil {
		return nil, err
	}

	if completedAtStr != "" {
		completedAt, err := time.Parse(time.RFC3339, completedAtStr)
		if err == nil {
			task.CompletedAt = &completedAt
		}
	}

	return task, nil
}

func (s *SQLiteStorage) GetAllTasks() ([]*model.Task, error) {
	query := `
	SELECT 
		id, url, name, status, progress, speed,
		thread_count, host_type, cookie, referer, auto_clear, save_path,
		output_path, enable_webdav, webdav_url, webdav_username,
		webdav_password, webdav_remote_dir, delete_after_upload,
		total_segments, downloaded_segments,
		created_at, completed_at
	FROM tasks ORDER BY created_at DESC
	`

	rows, err := s.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []*model.Task

	for rows.Next() {
		task := &model.Task{}
		var completedAtStr string

		err := rows.Scan(
			&task.ID,
			&task.URL,
			&task.Name,
			&task.Status,
			&task.Progress,
			&task.Speed,
			&task.ThreadCount,
			&task.HostType,
			&task.Cookie,
			&task.Referer,
			&task.AutoClear,
			&task.SavePath,
			&task.OutputPath,
			&task.EnableWebDAV,
			&task.WebDAVURL,
			&task.WebDAVUsername,
			&task.WebDAVPassword,
			&task.WebDAVRemoteDir,
			&task.DeleteAfterUpload,
			&task.TotalSegments,
			&task.DownloadedSegments,
			&task.CreatedAt,
			&completedAtStr,
		)

		if err != nil {
			return nil, err
		}

		if completedAtStr != "" {
			completedAt, err := time.Parse(time.RFC3339, completedAtStr)
			if err == nil {
				task.CompletedAt = &completedAt
			}
		}

		tasks = append(tasks, task)
	}

	return tasks, nil
}

func (s *SQLiteStorage) DeleteTask(taskID string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}

	_, err = tx.Exec("DELETE FROM logs WHERE task_id = ?", taskID)
	if err != nil {
		tx.Rollback()
		return err
	}

	_, err = tx.Exec("DELETE FROM tasks WHERE id = ?", taskID)
	if err != nil {
		tx.Rollback()
		return err
	}

	return tx.Commit()
}

func (s *SQLiteStorage) AddLog(taskID, level, message string) error {
	query := `
	INSERT INTO logs (task_id, level, message, timestamp)
	VALUES (?, ?, ?, ?)
	`

	_, err := s.db.Exec(
		query,
		taskID,
		level,
		message,
		time.Now().Format(time.RFC3339),
	)

	return err
}

func (s *SQLiteStorage) GetLogs(taskID string) ([]*model.LogEntry, error) {
	query := `
	SELECT level, message, timestamp FROM logs
	WHERE task_id = ? ORDER BY timestamp ASC
	`

	rows, err := s.db.Query(query, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var logs []*model.LogEntry

	for rows.Next() {
		logEntry := &model.LogEntry{}
		err := rows.Scan(&logEntry.Level, &logEntry.Message, &logEntry.Timestamp)
		if err != nil {
			return nil, err
		}
		logs = append(logs, logEntry)
	}

	return logs, nil
}

func (s *SQLiteStorage) Close() error {
	return s.db.Close()
}

func (s *SQLiteStorage) GetTaskCount(status string) (int, error) {
	query := "SELECT COUNT(*) FROM tasks"
	if status != "" {
		query += " WHERE status = ?"
	}

	var count int
	var err error

	if status != "" {
		err = s.db.QueryRow(query, status).Scan(&count)
	} else {
		err = s.db.QueryRow(query).Scan(&count)
	}

	return count, err
}

func (s *SQLiteStorage) GetRecentTasks(limit int) ([]*model.Task, error) {
	query := fmt.Sprintf(`
	SELECT 
		id, url, name, status, progress, speed,
		thread_count, host_type, cookie, auto_clear, save_path,
		output_path, total_segments, downloaded_segments,
		created_at, completed_at
	FROM tasks ORDER BY created_at DESC LIMIT %d
	`, limit)

	rows, err := s.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []*model.Task

	for rows.Next() {
		task := &model.Task{}
		var completedAtStr string

		err := rows.Scan(
			&task.ID,
			&task.URL,
			&task.Name,
			&task.Status,
			&task.Progress,
			&task.Speed,
			&task.ThreadCount,
			&task.HostType,
			&task.Cookie,
			&task.AutoClear,
			&task.SavePath,
			&task.OutputPath,
			&task.TotalSegments,
			&task.DownloadedSegments,
			&task.CreatedAt,
			&completedAtStr,
		)

		if err != nil {
			return nil, err
		}

		if completedAtStr != "" {
			completedAt, err := time.Parse(time.RFC3339, completedAtStr)
			if err == nil {
				task.CompletedAt = &completedAt
			}
		}

		tasks = append(tasks, task)
	}

	return tasks, nil
}