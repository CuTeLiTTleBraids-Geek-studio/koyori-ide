package services

// Plan 11 Task 7 — IM（即时通讯集成）。
//
// 支持 4 个 provider：Slack / Discord / 飞书 / 企业微信。
// 提供发送消息和 AI 主动通知；不提供 IM 入站能力。
//
// 安全模型（G-SEC-07 / G-SEC-12 / P19 P0-02）：
//   - Bot Token / Webhook URL 用 EncryptSecret 加密存储（AES-256-GCM / DPAPI）
//     LoadConfig 不回传明文，仅返回 configured 布尔（G-SEC-07）。
//   - IM 发送视同 Restricted 扩展能力，首次需审批（G-SEC-12）；
//     Webhook URL 变更即更换出站目的地，必须重新走原生同意边界。
//   - 配置文件 0600 + atomicWriteJSON（G-SEC-09）。
//   - 出站 HTTP 请求用 LimitReader 64KB 限制响应体（G-SEC-07）。
//   - Webhook URL 保存时经 ValidateNonPrivateURL 强校验（拒绝私网/环回/
//     链路本地/元数据地址），发送时经 SSRF 安全传输拨号复核（防 DNS
//     rebinding），不跟随重定向；与 MCP/AI/HTTPClient 的 C-1 姿态一致。
//   - wechat_work（企业微信群机器人）按官方规范经 `key` query 参数真实
//     发送 token。
//
// 通知规则：事件 → 频道 → Markdown 模板（Step 5/7）。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// ---------------------------------------------------------------------------
// 配置 schema（Step 1）
// ---------------------------------------------------------------------------

// IMConfig 是 IM 集成的持久化配置。
type IMConfig struct {
	Providers []IMProvider `json:"providers"`
	// NotificationRules 事件→频道→模板映射（Step 7）。
	NotificationRules []NotificationRule `json:"notificationRules,omitempty"`
	// Approved 表示用户已显式批准 IM 集成（G-SEC-12）。
	Approved bool `json:"-"`
}

type imPersistedConfig struct {
	Providers         []IMProvider       `json:"providers"`
	NotificationRules []NotificationRule `json:"notificationRules,omitempty"`
	ApprovalProof     string             `json:"approvalProof,omitempty"`
}

const imApprovalMarker = "koyori-ide-im-approved-v1"

func cloneIMConfig(cfg IMConfig) IMConfig {
	cloned := cfg
	cloned.Providers = append([]IMProvider(nil), cfg.Providers...)
	cloned.NotificationRules = append([]NotificationRule(nil), cfg.NotificationRules...)
	return cloned
}

// IMProvider 描述单个 IM provider 的连接配置（Step 1）。
type IMProvider struct {
	// Type provider 类型：slack / discord / feishu / wechat_work（Step 2）。
	Type string `json:"type"`
	// Name 用户自定义的实例名（允许同类型多实例）。
	Name string `json:"name"`
	// WebhookURL 出站 Webhook（用于发送消息）。加密存储。
	WebhookURL string `json:"webhookUrl,omitempty"`
	// BotToken bot 访问令牌（用于出站 API 调用）。加密存储。
	BotToken string `json:"botToken,omitempty"`
	// ChannelID 默认目标频道。
	ChannelID string `json:"channelId,omitempty"`
	// Enabled 是否启用该 provider。
	Enabled bool `json:"enabled"`
}

// NotificationRule 事件→频道→模板映射（Step 7）。
type NotificationRule struct {
	Event    string `json:"event"`    // task_completed / error_alert / review_result / daily_report
	Provider string `json:"provider"` // provider name
	Channel  string `json:"channel"`  // 目标频道 ID
	Template string `json:"template"` // Markdown 模板（含 {title}/{body}/{timestamp} 占位符）
	Enabled  bool   `json:"enabled"`
}

// 内置事件类型常量（Step 5）。
const (
	IMEventTaskCompleted = "task_completed"
	IMEventErrorAlert    = "error_alert"
	IMEventReviewResult  = "review_result"
	IMEventDailyReport   = "daily_report"
)

// ---------------------------------------------------------------------------
// IMService
// ---------------------------------------------------------------------------

// IMService 管理 IM 集成（Step 1-7）。
type IMService struct {
	mu            sync.RWMutex
	config        IMConfig
	configDir     string
	cfgPath       string
	http          *http.Client
	persistConfig func(IMConfig) error
	approve       func() bool
	approvalProof string
}

// newIMHTTPClient installs the production HTTP client for IM webhook
// delivery. It is a package variable only so tests can point the service at
// a local httptest fixture; the loopback refusal of the SSRF guard must
// never be weakened outside tests (P19 P0-02, C-1 alignment).
var newIMHTTPClient = func() *http.Client {
	return &http.Client{
		Timeout: 30 * time.Second,
		// Webhook endpoints are fixed destinations: never follow redirects.
		CheckRedirect: noRedirectPolicy,
		// SSRF-safe transport re-validates the resolved IP at dial time,
		// defeating DNS rebinding between URL validation and the request.
		Transport: NewSSRFSafeTransport(),
	}
}

// validateIMWebhookURL is the save-time URL policy for outbound webhooks.
// Package variable only so fixture tests can accept loopback httptest URLs;
// production uses ValidateNonPrivateURL (fail-closed on private/loopback).
var validateIMWebhookURL = ValidateNonPrivateURL

// NewIMService 创建服务。configDir 用于配置文件路径。
func NewIMService(configDir string) *IMService {
	svc := &IMService{
		configDir: configDir,
		cfgPath:   filepath.Join(configDir, "koyori-ide", "im.json"),
		http:      newIMHTTPClient(),
		approve:   nativeIMApproval,
	}
	_ = svc.loadConfig()
	return svc
}

func nativeIMApproval() bool {
	app := application.Get()
	if app == nil {
		return false
	}
	result := make(chan bool, 1)
	dialog := app.Dialog.Question().SetTitle("Approve IM integration").SetMessage(
		"Allow configured IM providers to send messages outside this application?",
	)
	dialog.AddButton("Approve").SetAsDefault().OnClick(func() { result <- true })
	dialog.AddButton("Cancel").SetAsCancel().OnClick(func() { result <- false })
	dialog.Show()
	select {
	case approved := <-result:
		return approved
	case <-time.After(5 * time.Minute):
		return false
	}
}

// LoadConfig 返回当前配置的副本，敏感字段替换为 configured 标记（G-SEC-07）。
// 明文 token/webhook 不返回前端，仅返回是否已配置。
func (s *IMService) LoadConfig() IMConfigView {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := IMConfigView{
		Approved:          s.config.Approved,
		NotificationRules: append([]NotificationRule{}, s.config.NotificationRules...),
	}
	for _, p := range s.config.Providers {
		out.Providers = append(out.Providers, IMProviderView{
			Type:               p.Type,
			Name:               p.Name,
			ChannelID:          p.ChannelID,
			Enabled:            p.Enabled,
			WebhookConfigured:  p.WebhookURL != "",
			BotTokenConfigured: p.BotToken != "",
		})
	}
	return out
}

// loadConfig 从磁盘加载配置并解密敏感字段。
func (s *IMService) loadConfig() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.cfgPath)
	if err != nil {
		if os.IsNotExist(err) {
			s.config = IMConfig{}
			return nil
		}
		return fmt.Errorf("read im config: %w", err)
	}
	var persisted imPersistedConfig
	if err := json.Unmarshal(data, &persisted); err != nil {
		return fmt.Errorf("parse im config: %w", err)
	}
	cfg := IMConfig{
		Providers:         persisted.Providers,
		NotificationRules: persisted.NotificationRules,
	}
	if proof, err := DecryptSecret(keyringAccount, persisted.ApprovalProof); err == nil && proof == imApprovalMarker {
		cfg.Approved = true
		s.approvalProof = persisted.ApprovalProof
	}
	// 解密敏感字段（best-effort；失败保留密文，后续操作会被拒绝）。
	for i := range cfg.Providers {
		if cfg.Providers[i].WebhookURL != "" {
			if plain, err := DecryptSecret(keyringAccount, cfg.Providers[i].WebhookURL); err == nil {
				cfg.Providers[i].WebhookURL = plain
			}
		}
		if cfg.Providers[i].BotToken != "" {
			if plain, err := DecryptSecret(keyringAccount, cfg.Providers[i].BotToken); err == nil {
				cfg.Providers[i].BotToken = plain
			}
		}
	}
	s.config = cloneIMConfig(cfg)
	return nil
}

// saveConfig 加密敏感字段后持久化（G-SEC-07 / G-SEC-09）。
func (s *IMService) saveConfig() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveConfigLocked()
}

func (s *IMService) saveConfigLocked() error {
	// 拷贝并加密敏感字段，避免修改内存中的明文。
	configCopy := cloneIMConfig(s.config)
	for i := range configCopy.Providers {
		if configCopy.Providers[i].WebhookURL != "" {
			enc, err := EncryptSecret(keyringAccount, configCopy.Providers[i].WebhookURL)
			if err != nil {
				return fmt.Errorf("encrypt webhook for %s: %w", configCopy.Providers[i].Name, err)
			}
			configCopy.Providers[i].WebhookURL = enc
		}
		if configCopy.Providers[i].BotToken != "" {
			enc, err := EncryptSecret(keyringAccount, configCopy.Providers[i].BotToken)
			if err != nil {
				return fmt.Errorf("encrypt token for %s: %w", configCopy.Providers[i].Name, err)
			}
			configCopy.Providers[i].BotToken = enc
		}
	}
	if s.persistConfig != nil {
		return s.persistConfig(configCopy)
	}
	return atomicWriteJSON(s.cfgPath, imPersistedConfig{
		Providers:         configCopy.Providers,
		NotificationRules: configCopy.NotificationRules,
		ApprovalProof:     s.approvalProof,
	}, 0600)
}

// UpdateConfig 更新配置并持久化（Step 1 / G-SEC-07 / G-SEC-09）。
// 首次启用（Approved=false → true）需用户显式确认（G-SEC-12）。
// Webhook URL 在保存阶段强校验（拒绝私网/环回/元数据目标）；任何 provider
// 的 Webhook URL 发生变更即更换了出站目的地，撤销已批准状态并要求重新走
// 原生同意边界（P19 P0-02）。
// imProviderTypes is the fail-closed whitelist of supported provider types
// (P20 P1-03). Anything outside it must be rejected at config save and at
// send time: an unknown Type used to have its BotToken encrypted and stored
// but silently never sent — the same class of defect as the wechat_work
// token that was silently dropped before P19 P0-02.
var imProviderTypes = map[string]bool{
	"slack":       true,
	"discord":     true,
	"feishu":      true,
	"wechat_work": true,
}

func (s *IMService) UpdateConfig(cfg IMConfig) error {
	for i := range cfg.Providers {
		p := &cfg.Providers[i]
		if p.WebhookURL != "" {
			if _, err := validateIMWebhookURL(p.WebhookURL); err != nil {
				return fmt.Errorf("provider %s webhook url rejected: %w", p.Name, err)
			}
		}
		// P20 P1-03: reject unknown provider types at save time so a
		// silently-dead provider (token stored, never sent) cannot persist.
		// Draft providers with neither webhook nor token stay allowed.
		if (p.WebhookURL != "" || p.BotToken != "") && !imProviderTypes[p.Type] {
			return fmt.Errorf("provider %s has unsupported type %q (fail-closed)", p.Name, p.Type)
		}
	}
	// Approval is a backend-owned capability. Renderer DTOs cannot grant it by
	// setting Approved=true; the dedicated Approve flow is the only grant path.
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := cloneIMConfig(s.config)
	previousProof := s.approvalProof
	cfg.Approved = s.config.Approved
	if imWebhookDestinationsChanged(previous.Providers, cfg.Providers) {
		cfg.Approved = false
		s.approvalProof = ""
	}
	s.config = cloneIMConfig(cfg)
	if err := s.saveConfigLocked(); err != nil {
		s.config = previous
		s.approvalProof = previousProof
		return err
	}
	return nil
}

// imWebhookDestinationsChanged reports whether any provider's outbound
// webhook destination changed (edited URL, or a new provider carrying a
// webhook). Provider identity is the user-chosen Name.
func imWebhookDestinationsChanged(previous, next []IMProvider) bool {
	previousURLs := make(map[string]string, len(previous))
	for _, p := range previous {
		previousURLs[p.Name] = p.WebhookURL
	}
	for _, p := range next {
		if p.WebhookURL != "" && previousURLs[p.Name] != p.WebhookURL {
			return true
		}
	}
	return false
}

// IsApproved 返回 IM 集成是否已获用户批准（G-SEC-12）。
func (s *IMService) IsApproved() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.config.Approved
}

// Approve obtains approval from a backend-owned native dialog before persisting it.
// Renderer confirmation state is not a security boundary.
func (s *IMService) Approve() (err error) {
	defer func() {
		outcome := "success"
		if err != nil {
			outcome = "failure"
		}
		securityAudit("im.approval", outcome)
	}()
	if s.approve == nil || !s.approve() {
		return fmt.Errorf("im approval rejected: %w", ErrNotAllowed)
	}
	proof, err := EncryptSecret(keyringAccount, imApprovalMarker)
	if err != nil {
		return fmt.Errorf("issue im approval proof: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	previousApproved, previousProof := s.config.Approved, s.approvalProof
	s.config.Approved = true
	s.approvalProof = proof
	if err := s.saveConfigLocked(); err != nil {
		s.config.Approved, s.approvalProof = previousApproved, previousProof
		return err
	}
	return nil
}

// ---------------------------------------------------------------------------
// 发送消息（Step 3）
// ---------------------------------------------------------------------------

// SendMessage 向指定 provider 的频道发送消息（Step 3）。
// attachments 为代码片段卡片（按 provider 格式化为 Markdown code block）。
// G-SEC-12：首次发送需 Approved=true。
func (s *IMService) SendMessage(ctx context.Context, providerName, channel, text string, attachments []string) (err error) {
	attachmentBytes := 0
	for _, attachment := range attachments {
		attachmentBytes += len(attachment)
	}
	defer func() {
		outcome := "success"
		if err != nil {
			outcome = "failure"
		}
		securityAudit("im.send", outcome,
			"provider", providerName, "text_bytes", len(text),
			"attachment_count", len(attachments), "attachment_bytes", attachmentBytes)
	}()
	if !s.IsApproved() {
		return fmt.Errorf("im not approved (G-SEC-12): %w", ErrNotAllowed)
	}
	s.mu.RLock()
	var provider IMProvider
	found := false
	for i := range s.config.Providers {
		if s.config.Providers[i].Name == providerName && s.config.Providers[i].Enabled {
			provider = s.config.Providers[i]
			found = true
			break
		}
	}
	s.mu.RUnlock()
	if !found {
		return fmt.Errorf("provider %q not found or disabled: %w", providerName, ErrNotFound)
	}
	if channel == "" {
		channel = provider.ChannelID
	}
	payload := s.buildSendPayload(provider.Type, channel, text, attachments)
	return s.sendToProvider(ctx, &provider, payload)
}

// buildSendPayload 按 provider 类型构造消息 payload（Step 3）。
func (s *IMService) buildSendPayload(providerType, channel, text string, attachments []string) map[string]interface{} {
	body := text
	for _, a := range attachments {
		body += "\n```\n" + a + "\n```"
	}
	switch providerType {
	case "slack":
		return map[string]interface{}{
			"channel": channel,
			"text":    body,
		}
	case "discord":
		return map[string]interface{}{
			"content": body,
		}
	case "feishu", "wechat_work":
		return map[string]interface{}{
			"msg_type": "text",
			"content": map[string]interface{}{
				"text": body,
			},
		}
	default:
		return map[string]interface{}{"text": body}
	}
}

// sendToProvider 通过 HTTP POST 发送到 provider Webhook（G-SEC-07：64KB 限制）。
// 传输层为 SSRF 安全 client（拨号复核 + 不跟随重定向，P19 P0-02）。
func (s *IMService) sendToProvider(ctx context.Context, provider *IMProvider, payload map[string]interface{}) error {
	if provider.WebhookURL == "" {
		return fmt.Errorf("provider %s has no webhook URL configured: %w", provider.Name, ErrInvalidInput)
	}
	// P20 P1-03: fail closed on unknown provider types — covers legacy
	// persisted configs saved before the UpdateConfig whitelist existed.
	// Without this, a bot token could be stored but silently never sent,
	// or a payload shaped for the wrong provider API could leak outbound.
	if !imProviderTypes[provider.Type] {
		return fmt.Errorf("provider %s has unsupported type %q (fail-closed): %w", provider.Name, provider.Type, ErrNotAllowed)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal im payload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, provider.WebhookURL, strings.NewReader(string(body)))
	if err != nil {
		return fmt.Errorf("build im request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if provider.BotToken != "" {
		switch provider.Type {
		case "slack":
			req.Header.Set("Authorization", "Bearer "+provider.BotToken)
		case "discord":
			req.Header.Set("Authorization", "Bot "+provider.BotToken)
		case "feishu":
			req.Header.Set("Authorization", "Bearer "+provider.BotToken)
		case "wechat_work":
			// 企业微信群机器人按官方规范经 `key` query 参数认证：把配置的
			// token 真实放入请求 URL（覆盖用户可能手填的旧 key）。
			u, perr := url.Parse(provider.WebhookURL)
			if perr != nil {
				return fmt.Errorf("parse im webhook url: %w", perr)
			}
			q := u.Query()
			q.Set("key", provider.BotToken)
			u.RawQuery = q.Encode()
			req.URL = u
		}
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return fmt.Errorf("im request failed: %w", err)
	}
	defer resp.Body.Close()
	// G-SEC-07：限制响应体 64KB，防止内存爆炸。
	limited := io.LimitReader(resp.Body, 64*1024)
	respBody, _ := io.ReadAll(limited)
	if resp.StatusCode >= 300 {
		// 3xx 一律失败：client 不跟随重定向（防止把 token/payload 带到
		// 非预期目的地），2xx 之外的任何状态都不算成功。
		return fmt.Errorf("im provider returned %d (redirects are not followed): %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// ---------------------------------------------------------------------------
// AI 主动通知（Step 5 / Step 7）
// ---------------------------------------------------------------------------

// Notify 发送事件通知（Step 5）。
// 根据 NotificationRules 匹配 event，渲染模板后发送到对应频道。
func (s *IMService) Notify(ctx context.Context, event, title, body string) error {
	if !s.IsApproved() {
		return fmt.Errorf("im not approved (G-SEC-12): %w", ErrNotAllowed)
	}
	s.mu.RLock()
	rules := append([]NotificationRule{}, s.config.NotificationRules...)
	s.mu.RUnlock()
	var errs []string
	for _, rule := range rules {
		if !rule.Enabled || rule.Event != event {
			continue
		}
		rendered := s.renderTemplate(rule.Template, title, body)
		if err := s.SendMessage(ctx, rule.Provider, rule.Channel, rendered, nil); err != nil {
			errs = append(errs, fmt.Sprintf("%s/%s: %v", rule.Provider, rule.Channel, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("notify errors: %s", strings.Join(errs, "; "))
	}
	return nil
}

// renderTemplate 渲染 Markdown 模板（Step 7）。
// 支持 {title} / {body} / {timestamp} 占位符。
func (s *IMService) renderTemplate(template, title, body string) string {
	out := strings.ReplaceAll(template, "{title}", title)
	out = strings.ReplaceAll(out, "{body}", body)
	out = strings.ReplaceAll(out, "{timestamp}", time.Now().UTC().Format(time.RFC3339))
	return out
}

// ---------------------------------------------------------------------------
// 视图结构（G-SEC-07：不回传明文敏感字段）
// ---------------------------------------------------------------------------

// IMConfigView 是返回前端的配置视图（敏感字段替换为 configured 布尔）。
type IMConfigView struct {
	Providers         []IMProviderView   `json:"providers"`
	NotificationRules []NotificationRule `json:"notificationRules,omitempty"`
	Approved          bool               `json:"approved"`
}

// IMProviderView 是 provider 的视图（G-SEC-07：不回传明文 token/webhook）。
type IMProviderView struct {
	Type               string `json:"type"`
	Name               string `json:"name"`
	ChannelID          string `json:"channelId"`
	Enabled            bool   `json:"enabled"`
	WebhookConfigured  bool   `json:"webhookConfigured"`
	BotTokenConfigured bool   `json:"botTokenConfigured"`
}
