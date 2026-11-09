// Package store 后台页那三块配置的落库处（SQLite 单文件）。
//
// 为什么用库而不是回写 YAML：页面上改一个值要写回带注释的 YAML，注释会丢、格式会漂，
// 而且改错了整个文件读不进来，边车直接起不动——那是"改了个开关把服务改死"。
// 表结构刻意只覆盖页面上看得见的东西：闸门、点歌这些没上页面的参数仍走 YAML 出厂值，
// 不因为"反正建库了"就偷偷往里加列（配置面越大越没人能找到东西）。
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

type Conn struct {
	RoomID string `json:"roomId"`
	Cookie string `json:"cookie"`
}

type Voice struct {
	Engine string  `json:"engine"`
	BaseURL string `json:"baseUrl"`
	APIKey  string  `json:"apiKey"`
	Model   string  `json:"model"`
	Timbre  string  `json:"timbre"`
	Speed   float64 `json:"speed"`
	Device  string  `json:"device"`
}

// Rule 一行"什么时候 → 说什么"。顺序即优先级，由切片顺序决定。
type Rule struct {
	Moment  string `json:"moment"`
	Line    string `json:"line"`
	Enabled bool   `json:"enabled"`
}

type Settings struct {
	Conn
	Voice
	Rules []Rule `json:"rules"`
	Muted bool   `json:"muted"`
}

type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS conn (
	id INTEGER PRIMARY KEY CHECK (id = 1), room_id TEXT NOT NULL DEFAULT '', cookie TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS voice (
	id INTEGER PRIMARY KEY CHECK (id = 1), engine TEXT NOT NULL DEFAULT '', base_url TEXT NOT NULL DEFAULT '',
	api_key TEXT NOT NULL DEFAULT '', model TEXT NOT NULL DEFAULT '', timbre TEXT NOT NULL DEFAULT '',
	speed REAL NOT NULL DEFAULT 1, device TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS rule (
	ord INTEGER PRIMARY KEY, moment TEXT NOT NULL, line TEXT NOT NULL, enabled INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS state (
	id INTEGER PRIMARY KEY CHECK (id = 1), muted INTEGER NOT NULL DEFAULT 0
);
`

func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("建数据目录: %w", err)
		}
	}
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(3000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // 单文件边车：一个写连接最省事，也避开 SQLite 的写锁竞争
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("建表: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Save 整份覆盖：页面是"改完点保存"的模型，逐字段增量更新只会带来半保存状态。
func (s *Store) Save(st Settings) error {
	for i, r := range st.Rules {
		if strings.TrimSpace(r.Line) == "" {
			return fmt.Errorf("第 %d 行的话术是空的", i+1)
		}
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`INSERT INTO conn (id, room_id, cookie) VALUES (1, ?, ?)
		ON CONFLICT(id) DO UPDATE SET room_id = excluded.room_id, cookie = excluded.cookie`,
		st.Conn.RoomID, st.Conn.Cookie); err != nil {
		return fmt.Errorf("存连接: %w", err)
	}
	if _, err := tx.Exec(`INSERT INTO voice (id, engine, base_url, api_key, model, timbre, speed, device)
		VALUES (1, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET engine = excluded.engine, base_url = excluded.base_url,
			api_key = excluded.api_key, model = excluded.model, timbre = excluded.timbre,
			speed = excluded.speed, device = excluded.device`,
		st.Voice.Engine, st.Voice.BaseURL, st.Voice.APIKey, st.Voice.Model, st.Voice.Timbre, st.Voice.Speed, st.Voice.Device); err != nil {
		return fmt.Errorf("存声音: %w", err)
	}
	// 规则整表重写：顺序就是优先级，留着旧行做 diff 会出现"改了顺序但没改内容"的漏网。
	if _, err := tx.Exec(`DELETE FROM rule`); err != nil {
		return fmt.Errorf("清规则: %w", err)
	}
	for i, r := range st.Rules {
		e := 0
		if r.Enabled {
			e = 1
		}
		if _, err := tx.Exec(`INSERT INTO rule (ord, moment, line, enabled) VALUES (?, ?, ?, ?)`,
			i, r.Moment, r.Line, e); err != nil {
			return fmt.Errorf("存第 %d 条规则: %w", i+1, err)
		}
	}
	return tx.Commit()
}

func (s *Store) Load() (Settings, error) {
	var st Settings
	var muted int

	err := s.db.QueryRow(`SELECT room_id, cookie FROM conn WHERE id = 1`).Scan(&st.Conn.RoomID, &st.Conn.Cookie)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	} else if err != nil {
		return st, err
	}
	err = s.db.QueryRow(`SELECT engine, base_url, api_key, model, timbre, speed, device FROM voice WHERE id = 1`).
		Scan(&st.Voice.Engine, &st.Voice.BaseURL, &st.Voice.APIKey, &st.Voice.Model, &st.Voice.Timbre, &st.Voice.Speed, &st.Voice.Device)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	} else if err != nil {
		return st, err
	}
	err = s.db.QueryRow(`SELECT muted FROM state WHERE id = 1`).Scan(&muted)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	} else if err != nil {
		return st, err
	}
	st.Muted = muted != 0

	rows, err := s.db.Query(`SELECT moment, line, enabled FROM rule ORDER BY ord`)
	if err != nil {
		return st, err
	}
	defer rows.Close()
	for rows.Next() {
		var r Rule
		var e int
		if err := rows.Scan(&r.Moment, &r.Line, &e); err != nil {
			return st, err
		}
		r.Enabled = e != 0
		st.Rules = append(st.Rules, r)
	}
	return st, rows.Err()
}

// SetMuted 单独存闭麦状态：它不属于"配置"，是现场开关，重启后必须保持原样。
func (s *Store) SetMuted(v bool) error {
	m := 0
	if v {
		m = 1
	}
	_, err := s.db.Exec(`INSERT INTO state (id, muted) VALUES (1, ?)
		ON CONFLICT(id) DO UPDATE SET muted = excluded.muted`, m)
	return err
}

// Empty 库里一行都还没有（第一次跑）——由调用方决定拿 YAML 出厂值来播种。
func (s *Store) Empty() (bool, error) {
	var c, v, r int
	if err := s.db.QueryRow(`SELECT COUNT(1) FROM conn`).Scan(&c); err != nil {
		return false, err
	}
	if err := s.db.QueryRow(`SELECT COUNT(1) FROM voice`).Scan(&v); err != nil {
		return false, err
	}
	if err := s.db.QueryRow(`SELECT COUNT(1) FROM rule`).Scan(&r); err != nil {
		return false, err
	}
	return c == 0 && v == 0 && r == 0, nil
}
