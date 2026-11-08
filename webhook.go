package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"time"
)

// fireWebhook 把任务事件异步推给用户配置的地址。
// 只要 WebhookURL 为空就什么也不做，零开销。
func (m *Manager) fireWebhook(event string, t *Task) {
	m.mu.Lock()
	target := m.st.WebhookURL
	secret := m.st.WebhookSecret
	m.mu.Unlock()
	if target == "" || t == nil {
		return
	}

	payload := map[string]any{
		"event": event,
		"time":  time.Now().UnixMilli(),
		"task":  t,
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return
	}

	go func() {
		client := &http.Client{Timeout: 8 * time.Second}
		for attempt := 0; attempt < 2; attempt++ {
			req, err := http.NewRequest(http.MethodPost, target, bytes.NewReader(b))
			if err != nil {
				return
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("User-Agent", "vidfetch/"+appVersion)
			req.Header.Set("X-VF-Event", event)
			if secret != "" {
				mac := hmac.New(sha256.New, []byte(secret))
				mac.Write(b)
				req.Header.Set("X-VF-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
			}
			resp, err := client.Do(req)
			if err == nil {
				io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
				resp.Body.Close()
				if resp.StatusCode < 400 {
					return
				}
				log.Printf("webhook 返回 %d (%s)", resp.StatusCode, event)
			} else {
				log.Printf("webhook 推送失败 (%s): %v", event, err)
			}
			time.Sleep(2 * time.Second)
		}
	}()
}
