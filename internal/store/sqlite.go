package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	_ "modernc.org/sqlite" // pure Go, so shoal still builds without a C compiler
)

// NetworkID recognises a network across visits. The subnet alone is not
// enough: 192.168.1.0/24 behind a router at .1 describes half the venues in
// the world. The gateway's MAC is what tells one of them from another.
type NetworkID struct {
	Subnet     string
	GatewayMAC string // empty on a network without a gateway
}

// String is the key the history is stored under.
func (n NetworkID) String() string {
	if n.GatewayMAC == "" {
		return n.Subnet + " (no gateway)"
	}
	return n.Subnet + " via " + n.GatewayMAC
}

// Network is what the history holds about one network.
type Network struct {
	ID         NetworkID
	GatewayIP  string
	FirstVisit time.Time
	LastVisit  time.Time
	Visits     int
}

// Remembered is what the history holds about one device on one network.
type Remembered struct {
	MAC        string
	IP         string
	Hostname   string
	Vendor     string
	FirstSeen  time.Time
	LastSeen   time.Time
	LastSource string // the probe that last heard from it
}

// History is shoal's memory of the networks it has scanned, in a SQLite
// file. Only the history probe writes to it; everything it recalls reaches
// the table as observations like any other probe's.
type History struct {
	db   *sql.DB
	path string
}

const schema = `
CREATE TABLE IF NOT EXISTS networks (
	id          TEXT PRIMARY KEY,
	subnet      TEXT NOT NULL,
	gateway_mac TEXT NOT NULL,
	gateway_ip  TEXT NOT NULL DEFAULT '',
	first_visit INTEGER NOT NULL,
	last_visit  INTEGER NOT NULL,
	visits      INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS devices (
	network     TEXT NOT NULL REFERENCES networks(id),
	mac         TEXT NOT NULL,
	ip          TEXT NOT NULL DEFAULT '',
	hostname    TEXT NOT NULL DEFAULT '',
	vendor      TEXT NOT NULL DEFAULT '',
	first_seen  INTEGER NOT NULL,
	last_seen   INTEGER NOT NULL,
	last_source TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (network, mac)
);
PRAGMA user_version = 1;
`

// DefaultHistoryPath is where the history lives unless --history says
// otherwise: the per-user data directory each platform expects.
func DefaultHistoryPath() (string, error) {
	if dir := os.Getenv("XDG_DATA_HOME"); dir != "" {
		return filepath.Join(dir, "shoal", "history.db"), nil
	}
	if runtime.GOOS == "windows" {
		if dir := os.Getenv("LocalAppData"); dir != "" {
			return filepath.Join(dir, "shoal", "history.db"), nil
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("no home directory for the history file: %w", err)
	}
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Application Support", "shoal", "history.db"), nil
	}
	return filepath.Join(home, ".local", "share", "shoal", "history.db"), nil
}

// OpenHistory opens or creates the history file. The directory is created
// readable by this user only: the file lists the devices on every network
// scanned. An empty path keeps the history in memory, for the demo.
func OpenHistory(path string) (*History, error) {
	dsn := "file::memory:?cache=shared"
	if path != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, fmt.Errorf("history directory: %w", err)
		}
		dsn = "file:" + filepath.ToSlash(path)
	}
	db, err := sql.Open("sqlite", dsn+sep(dsn)+"_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, fmt.Errorf("open history %s: %w", path, err)
	}
	db.SetMaxOpenConns(1) // one writer; also keeps an in-memory database alive
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("history %s: %w", path, err)
	}
	return &History{db: db, path: path}, nil
}

func sep(dsn string) string {
	for _, c := range dsn {
		if c == '?' {
			return "&"
		}
	}
	return "?"
}

// Path is where the history is kept; empty when it is in memory.
func (h *History) Path() string { return h.path }

// Close closes the file.
func (h *History) Close() error { return h.db.Close() }

// Visit records that a visit to the network has begun and returns what was
// known before it: the network as it stood (zero Visits on a first visit)
// and every device remembered on it. Comparisons are made against this, so
// a device recorded during this visit is never "known" to itself.
func (h *History) Visit(id NetworkID, gatewayIP string, at time.Time) (Network, []Remembered, error) {
	prev, known, err := h.Recall(id)
	if err != nil {
		return Network{}, nil, err
	}
	_, err = h.db.Exec(`INSERT INTO networks (id, subnet, gateway_mac, gateway_ip, first_visit, last_visit, visits)
		VALUES (?, ?, ?, ?, ?, ?, 1)
		ON CONFLICT (id) DO UPDATE SET
			gateway_ip = excluded.gateway_ip,
			last_visit = excluded.last_visit,
			visits     = visits + 1`,
		id.String(), id.Subnet, id.GatewayMAC, gatewayIP, millis(at), millis(at))
	if err != nil {
		return Network{}, nil, fmt.Errorf("history: %w", err)
	}
	return prev, known, nil
}

// Networks lists every network remembered, most recently visited first.
func (h *History) Networks() ([]Network, error) {
	rows, err := h.db.Query(`SELECT subnet, gateway_mac, gateway_ip, first_visit, last_visit, visits
		FROM networks ORDER BY last_visit DESC`)
	if err != nil {
		return nil, fmt.Errorf("history: %w", err)
	}
	defer rows.Close()
	var out []Network
	for rows.Next() {
		var n Network
		var first, last int64
		if err := rows.Scan(&n.ID.Subnet, &n.ID.GatewayMAC, &n.GatewayIP, &first, &last, &n.Visits); err != nil {
			return nil, fmt.Errorf("history: %w", err)
		}
		n.FirstVisit, n.LastVisit = fromMillis(first), fromMillis(last)
		out = append(out, n)
	}
	return out, rows.Err()
}

// Recall returns what is remembered about a network without counting a
// visit: the network (zero Visits if never seen) and its devices.
func (h *History) Recall(id NetworkID) (Network, []Remembered, error) {
	key := id.String()
	prev := Network{ID: id}
	var first, last int64
	err := h.db.QueryRow(`SELECT gateway_ip, first_visit, last_visit, visits FROM networks WHERE id = ?`, key).
		Scan(&prev.GatewayIP, &first, &last, &prev.Visits)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return Network{}, nil, fmt.Errorf("history: %w", err)
	default:
		prev.FirstVisit, prev.LastVisit = fromMillis(first), fromMillis(last)
	}

	rows, err := h.db.Query(`SELECT mac, ip, hostname, vendor, first_seen, last_seen, last_source
		FROM devices WHERE network = ? ORDER BY mac`, key)
	if err != nil {
		return Network{}, nil, fmt.Errorf("history: %w", err)
	}
	var known []Remembered
	for rows.Next() {
		var r Remembered
		var fs, ls int64
		if err := rows.Scan(&r.MAC, &r.IP, &r.Hostname, &r.Vendor, &fs, &ls, &r.LastSource); err != nil {
			rows.Close()
			return Network{}, nil, fmt.Errorf("history: %w", err)
		}
		r.FirstSeen, r.LastSeen = fromMillis(fs), fromMillis(ls)
		known = append(known, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return Network{}, nil, fmt.Errorf("history: %w", err)
	}
	return prev, known, nil
}

// Record saves what is known about devices now, in one transaction. Empty
// strings leave the remembered value alone, so a name learned yesterday is
// not wiped by a scan that has not asked yet; first seen only moves earlier
// and last seen only later.
func (h *History) Record(id NetworkID, devices ...Remembered) error {
	tx, err := h.db.Begin()
	if err != nil {
		return fmt.Errorf("history: %w", err)
	}
	for _, r := range devices {
		if err := record(tx, id, r); err != nil {
			tx.Rollback()
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("history: %w", err)
	}
	return nil
}

func record(tx *sql.Tx, id NetworkID, r Remembered) error {
	_, err := tx.Exec(`INSERT INTO devices (network, mac, ip, hostname, vendor, first_seen, last_seen, last_source)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (network, mac) DO UPDATE SET
			ip          = CASE WHEN excluded.ip       <> '' THEN excluded.ip       ELSE ip       END,
			hostname    = CASE WHEN excluded.hostname <> '' THEN excluded.hostname ELSE hostname END,
			vendor      = CASE WHEN excluded.vendor   <> '' THEN excluded.vendor   ELSE vendor   END,
			last_source = CASE WHEN excluded.last_seen >= last_seen THEN excluded.last_source ELSE last_source END,
			first_seen  = MIN(first_seen, excluded.first_seen),
			last_seen   = MAX(last_seen, excluded.last_seen)`,
		id.String(), r.MAC, r.IP, r.Hostname, r.Vendor, millis(r.FirstSeen), millis(r.LastSeen), r.LastSource)
	if err != nil {
		return fmt.Errorf("history: record %s: %w", r.MAC, err)
	}
	return nil
}

func millis(t time.Time) int64      { return t.UnixMilli() }
func fromMillis(ms int64) time.Time { return time.UnixMilli(ms) }
