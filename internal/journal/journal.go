// Package journal is the engine's record of downloads, kept in a SQLite
// database in WAL mode, so the list and the progress of each file survive the
// engine stopping or crashing.
//
// Progress is kept per byte range. The engine flushes the file to disk before
// it commits the ranges written, so a committed range is always really on the
// disk; a resumed download fetches only what isn't committed.
//
// A download's request (with its cookies and headers), or a stream's, is
// kept only while the download can still be resumed, and is wiped when it
// ends. Streams the extension found, with their cookies, are kept for as long
// as the engine offers them (a day), then deleted.
package journal

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/xoGUYFAWKSxo/internet-downloader/internal/fetch"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/media"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/outfile"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/probe"
)

const schema = `
CREATE TABLE IF NOT EXISTS downloads (
	id        INTEGER PRIMARY KEY,
	status    TEXT NOT NULL,           -- fetch.Status as JSON
	request   TEXT NOT NULL DEFAULT '', -- probe.Request as JSON, '' once it can't resume
	part      TEXT NOT NULL DEFAULT '', -- the .part file
	size      INTEGER NOT NULL DEFAULT -1,
	validator TEXT NOT NULL DEFAULT ''  -- ETag or Last-Modified the bytes came from
);
CREATE TABLE IF NOT EXISTS ranges (
	download INTEGER NOT NULL REFERENCES downloads(id) ON DELETE CASCADE,
	start    INTEGER NOT NULL,
	"end"    INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS ranges_download ON ranges(download);
CREATE TABLE IF NOT EXISTS settings (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS received (
	id   INTEGER PRIMARY KEY,  -- the engine's media.Item ID
	at   INTEGER NOT NULL,     -- when it came, Unix seconds
	item TEXT NOT NULL         -- media.Item as JSON, cookies included
);
`

// columns are added to downloads after its first version; adding one that
// is already there fails harmlessly.
var columns = []string{
	`ALTER TABLE downloads ADD COLUMN dir TEXT NOT NULL DEFAULT ''`,  // the folder chosen to save into
	`ALTER TABLE downloads ADD COLUMN name TEXT NOT NULL DEFAULT ''`, // the file name chosen
	// A stream's request and how far it got, as the engine's JSON; '' for
	// files, and once the stream can't resume.
	`ALTER TABLE downloads ADD COLUMN stream TEXT NOT NULL DEFAULT ''`,
}

type Journal struct{ db *sql.DB }

// Record is one download as saved.
type Record struct {
	Status    fetch.Status
	Request   *probe.Request // nil once the download can't be resumed
	Part      string
	Size      int64
	Validator string
	Have      []outfile.Range
	Dir, Name string // where the user chose to save it; "" for the default
	Stream    string // a stream's state to resume it, or ""
}

// Open opens (or creates) the database at path.
func Open(path string) (*Journal, error) {
	dsn := path + "?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// One engine writes; one connection keeps the pragmas and avoids locks.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("couldn't open the download list (%s): %w", path, err)
	}
	for _, c := range columns {
		if _, err := db.Exec(c); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			db.Close()
			return nil, fmt.Errorf("couldn't update the download list (%s): %w", path, err)
		}
	}
	return &Journal{db: db}, nil
}

func (j *Journal) Close() error { return j.db.Close() }

// Add saves a new download. r is nil for one that can't be resumed.
func (j *Journal) Add(s fetch.Status, r *probe.Request) error {
	st, req, err := encode(s, r)
	if err != nil {
		return err
	}
	_, err = j.db.Exec(`INSERT INTO downloads (id, status, request) VALUES (?, ?, ?)`, s.ID, st, req)
	return err
}

// SetTarget saves the folder and file name the user chose for a download.
func (j *Journal) SetTarget(id uint64, dir, name string) error {
	_, err := j.db.Exec(`UPDATE downloads SET dir = ?, name = ? WHERE id = ?`, dir, name, id)
	return err
}

// SetStream saves a stream's state, which holds its request.
func (j *Journal) SetStream(id uint64, state string) error {
	_, err := j.db.Exec(`UPDATE downloads SET stream = ? WHERE id = ?`, state, id)
	return err
}

// Setting gives a saved setting, or def when it was never set.
func (j *Journal) Setting(key, def string) string {
	var v string
	if j.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v) != nil {
		return def
	}
	return v
}

// SetSetting saves a setting.
func (j *Journal) SetSetting(key, value string) error {
	_, err := j.db.Exec(`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// SetStatus saves what the window shows of a download.
func (j *Journal) SetStatus(s fetch.Status) error {
	st, _, err := encode(s, nil)
	if err != nil {
		return err
	}
	_, err = j.db.Exec(`UPDATE downloads SET status = ? WHERE id = ?`, st, s.ID)
	return err
}

// SetFile saves which file a download writes into, its size and the
// server's validator, and forgets ranges from an earlier file.
func (j *Journal) SetFile(id uint64, part string, size int64, validator string) error {
	tx, err := j.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE downloads SET part = ?, size = ?, validator = ? WHERE id = ?`, part, size, validator, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM ranges WHERE download = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// Commit replaces a download's committed ranges with have. Call it only
// after have is flushed to disk.
func (j *Journal) Commit(id uint64, have []outfile.Range) error {
	tx, err := j.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM ranges WHERE download = ?`, id); err != nil {
		return err
	}
	for _, r := range have {
		if _, err := tx.Exec(`INSERT INTO ranges (download, start, "end") VALUES (?, ?, ?)`, id, r.Start, r.End); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// End saves a download's final status and wipes what was kept to resume it:
// the request (cookies included), a stream's state, the file and the ranges.
func (j *Journal) End(s fetch.Status) error {
	st, _, err := encode(s, nil)
	if err != nil {
		return err
	}
	tx, err := j.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE downloads SET status = ?, request = '', stream = '', part = '', validator = '' WHERE id = ?`, st, s.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM ranges WHERE download = ?`, s.ID); err != nil {
		return err
	}
	return tx.Commit()
}

// SaveReceived keeps a stream the extension found, and forgets the oldest
// beyond keep and those found before since.
func (j *Journal) SaveReceived(it media.Item, keep int, since time.Time) error {
	b, err := json.Marshal(it)
	if err != nil {
		return err
	}
	tx, err := j.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT OR REPLACE INTO received (id, at, item) VALUES (?, ?, ?)`, it.ID, it.Received.Unix(), string(b)); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM received WHERE at < ? OR id NOT IN (SELECT id FROM received ORDER BY id DESC LIMIT ?)`, since.Unix(), keep); err != nil {
		return err
	}
	return tx.Commit()
}

// LoadReceived deletes the streams found before since and gives the others,
// oldest first.
func (j *Journal) LoadReceived(since time.Time) ([]media.Item, error) {
	if _, err := j.db.Exec(`DELETE FROM received WHERE at < ?`, since.Unix()); err != nil {
		return nil, err
	}
	rows, err := j.db.Query(`SELECT item FROM received ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []media.Item
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		var it media.Item
		if json.Unmarshal([]byte(s), &it) == nil && it.ID != 0 {
			out = append(out, it)
		}
	}
	return out, rows.Err()
}

// Delete forgets a download, with its ranges.
func (j *Journal) Delete(id uint64) error {
	_, err := j.db.Exec(`DELETE FROM downloads WHERE id = ?`, id)
	return err
}

// Load gives every saved download, oldest first.
func (j *Journal) Load() ([]Record, error) {
	rows, err := j.db.Query(`SELECT id, status, request, part, size, validator, dir, name, stream FROM downloads ORDER BY id`)
	if err != nil {
		return nil, err
	}
	var out []Record
	for rows.Next() {
		var id uint64
		var st, req string
		var r Record
		if err := rows.Scan(&id, &st, &req, &r.Part, &r.Size, &r.Validator, &r.Dir, &r.Name, &r.Stream); err != nil {
			rows.Close()
			return nil, err
		}
		if err := json.Unmarshal([]byte(st), &r.Status); err != nil {
			continue
		}
		r.Status.ID = id
		if req != "" {
			r.Request = new(probe.Request)
			if json.Unmarshal([]byte(req), r.Request) != nil {
				r.Request = nil
			}
		}
		out = append(out, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].Have, err = j.ranges(out[i].Status.ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (j *Journal) ranges(id uint64) ([]outfile.Range, error) {
	rows, err := j.db.Query(`SELECT start, "end" FROM ranges WHERE download = ? ORDER BY start`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []outfile.Range
	for rows.Next() {
		var r outfile.Range
		if err := rows.Scan(&r.Start, &r.End); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Trim deletes the oldest downloads that have ended, keeping at most keep
// downloads in all.
func (j *Journal) Trim(keep int) error {
	_, err := j.db.Exec(`DELETE FROM downloads WHERE request = '' AND stream = '' AND id NOT IN
		(SELECT id FROM downloads ORDER BY id DESC LIMIT ?)`, keep)
	return err
}

func encode(s fetch.Status, r *probe.Request) (string, string, error) {
	st, err := json.Marshal(s)
	if err != nil || r == nil {
		return string(st), "", err
	}
	req, err := json.Marshal(r)
	return string(st), string(req), err
}
