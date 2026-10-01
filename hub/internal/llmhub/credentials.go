package llmhub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type pendingCredential struct {
	provider, name, secret, keyID string
}

// Only key references enter the configuration transaction. Secret values travel
// directly to the private Bifrost key API and never enter SQLite or responses.
func (s *Server) saveConfiguration(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Config  Config `json:"config"`
		Version int64  `json:"version"`
	}
	if err := readJSON(w, r, &body); err != nil {
		writeError(w, 400, "invalid_config", "配置格式不正确，请检查字段类型")
		return
	}
	var pending []pendingCredential
	groups := [][]ProviderCredential{body.Config.DefaultCredentials}
	for _, p := range body.Config.Projects {
		groups = append(groups, p.Credentials)
	}
	for _, bindings := range groups {
		for i := range bindings {
			b := &bindings[i]
			if b.APIKey == "" {
				continue
			}
			secret := b.APIKey
			b.APIKey = ""
			if len(secret) > 4096 || strings.TrimSpace(secret) != secret || strings.IndexFunc(secret, func(r rune) bool { return r <= 32 || r == 127 }) >= 0 || strings.HasPrefix(secret, "env.") {
				writeError(w, 400, "invalid_config", fmt.Sprintf("平台 %s 的 API Key：请粘贴平台颁发的完整密钥，不要包含空白或环境变量引用", b.Provider))
				return
			}
			b.KeyName = randomID("llmhub-")
			pending = append(pending, pendingCredential{provider: b.Provider, name: b.KeyName, secret: secret})
		}
	}
	if err := body.Config.Validate(); err != nil {
		writeError(w, 400, "invalid_config", err.Error())
		return
	}
	s.configMu.RLock()
	stale := body.Version != s.version
	s.configMu.RUnlock()
	if stale {
		writeError(w, 409, "version_conflict", "配置已被修改，请刷新后重试")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	saved := false
	defer func() {
		if saved {
			return
		}
		cleanup, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		for _, b := range pending {
			if b.keyID != "" {
				_, _ = s.credentialRequest(cleanup, "DELETE", "/api/providers/"+url.PathEscape(b.provider)+"/keys/"+url.PathEscape(b.keyID), nil)
			}
		}
	}()
	for i := range pending {
		b := &pending[i]
		payload, _ := json.Marshal(struct {
			Name   string   `json:"name"`
			Value  string   `json:"value"`
			Models []string `json:"models"`
			Weight int      `json:"weight"`
		}{b.name, b.secret, []string{"*"}, 1})
		raw, err := s.credentialRequest(ctx, "POST", "/api/providers/"+url.PathEscape(b.provider)+"/keys", payload)
		b.secret = ""
		if err != nil {
			writeError(w, 502, "credential_save_failed", fmt.Sprintf("平台 %s 的 API Key 未保存：%s。原配置未改变", b.provider, err))
			return
		}
		var result struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		if json.Unmarshal(raw, &result) != nil || result.ID == "" || result.Name != b.name {
			writeError(w, 502, "credential_save_failed", "API Key 保存结果无法确认，原配置未改变，请检查私有供应商网关")
			return
		}
		b.keyID = result.ID
	}
	// Network calls never hold the configuration lock. Recheck the version at
	// commit so concurrent edits are preserved and readers remain responsive.
	s.configMu.Lock()
	defer s.configMu.Unlock()
	version, err := s.store.SaveConfig(body.Config, body.Version)
	if err != nil {
		if errors.Is(err, ErrConflict) {
			writeError(w, 409, "version_conflict", "配置已被修改，请刷新后重试")
			return
		}
		s.fail(w, err)
		return
	}
	s.config, s.version = body.Config, version
	s.scheduler.Update(body.Config.Pools)
	saved = true
	writeJSON(w, 200, map[string]any{"config": body.Config, "version": version})
}

func (s *Server) credentialRequest(ctx context.Context, method, path string, body []byte) ([]byte, error) {
	req, _ := http.NewRequestWithContext(ctx, method, s.upstream.String()+path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if s.bifrostUser != "" {
		req.SetBasicAuth(s.bifrostUser, s.bifrostPassword)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("供应商网关未响应，请检查连接")
	}
	defer resp.Body.Close()
	// Never relay an upstream error body: it may echo a submitted secret.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		switch resp.StatusCode {
		case 401, 403:
			return nil, fmt.Errorf("供应商网关管理授权失败，请配置服务端 LLMHUB_BIFROST_USERNAME / LLMHUB_BIFROST_PASSWORD")
		case 404:
			return nil, fmt.Errorf("平台尚未在供应商网关启用，请先添加该平台")
		default:
			return nil, fmt.Errorf("供应商网关拒绝保存（HTTP %d）", resp.StatusCode)
		}
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (64<<10)+1))
	if err != nil || len(raw) > 64<<10 {
		return nil, fmt.Errorf("供应商网关返回格式异常")
	}
	return raw, nil
}
