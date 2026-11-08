package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	bolt "go.etcd.io/bbolt"
)

var (
	bucketTasks = []byte("tasks")
	bucketMeta  = []byte("meta")
)

// Store 是 bbolt 的薄封装：任务是高写入小对象，bbolt 的 B+Tree + 单文件
// 在小内存设备上比 SQLite/JSON 全量重写都更省。NoFreelistSync 省一次 fsync。
type Store struct {
	db *bolt.DB
}

func openStore(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := bolt.Open(path, 0o600, &bolt.Options{
		Timeout:         5 * time.Second,
		NoFreelistSync:  true,
		FreelistType:    bolt.FreelistMapType,
		InitialMmapSize: 1 << 20,
	})
	if err != nil {
		return nil, err
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		if _, err := tx.CreateBucketIfNotExists(bucketTasks); err != nil {
			return err
		}
		_, err := tx.CreateBucketIfNotExists(bucketMeta)
		return err
	}); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) putTask(t *Task) error {
	b, err := json.Marshal(t)
	if err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketTasks).Put([]byte(t.ID), b)
	})
}

func (s *Store) delTask(id string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketTasks).Delete([]byte(id))
	})
}

func (s *Store) allTasks() ([]*Task, error) {
	var out []*Task
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketTasks).ForEach(func(k, v []byte) error {
			t := &Task{}
			if err := json.Unmarshal(v, t); err != nil {
				return nil // 跳过损坏记录，不让一条脏数据拖死启动
			}
			out = append(out, t)
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt == out[j].CreatedAt {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt > out[j].CreatedAt
	})
	return out, nil
}

func (s *Store) metaGet(key string) string {
	var v string
	_ = s.db.View(func(tx *bolt.Tx) error {
		if b := tx.Bucket(bucketMeta).Get([]byte(key)); b != nil {
			v = string(b)
		}
		return nil
	})
	return v
}

func (s *Store) metaPut(key, val string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketMeta).Put([]byte(key), []byte(val))
	})
}

func (s *Store) loadSettings() (Settings, error) {
	st := defaultSettings()
	raw := s.metaGet("settings")
	if raw == "" {
		return st, nil
	}
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		return defaultSettings(), fmt.Errorf("settings 解析失败，已回退默认值: %w", err)
	}
	return st, nil
}

func (s *Store) saveSettings(st Settings) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return s.metaPut("settings", string(b))
}
