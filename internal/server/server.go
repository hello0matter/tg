package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"tgworkbench/internal/aiprocessor"
	"tgworkbench/internal/domain"
	"tgworkbench/internal/rules"
	"tgworkbench/internal/startup"
	"tgworkbench/internal/store"
	"tgworkbench/internal/vault"
)

type Runtime interface {
	Connect(accountID string) error
	Disconnect(accountID string) error
	SubmitCode(accountID, code string) error
	SubmitPassword(accountID, password string) error
	Approve(reviewID string) error
	SendManual(routeID, text string) error
	ListPeers(accountID string) ([]domain.PeerRef, error)
}

type Server struct {
	store   *store.Store
	vault   *vault.Vault
	runtime Runtime
	assets  fs.FS
	log     *slog.Logger
	engine  rules.Engine
}

func New(store *store.Store, vault *vault.Vault, runtime Runtime, assets fs.FS, log *slog.Logger) *Server {
	return &Server{store: store, vault: vault, runtime: runtime, assets: assets, log: log}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /api/dashboard", s.dashboard)
	mux.HandleFunc("GET /api/accounts", s.accounts)
	mux.HandleFunc("POST /api/accounts", s.createAccount)
	mux.HandleFunc("DELETE /api/accounts/{id}", s.deleteAccount)
	mux.HandleFunc("POST /api/accounts/{id}/connect", s.connectAccount)
	mux.HandleFunc("POST /api/accounts/{id}/disconnect", s.disconnectAccount)
	mux.HandleFunc("POST /api/accounts/{id}/code", s.submitCode)
	mux.HandleFunc("POST /api/accounts/{id}/password", s.submitPassword)
	mux.HandleFunc("GET /api/accounts/{id}/peers", s.accountPeers)
	mux.HandleFunc("GET /api/routes", s.routes)
	mux.HandleFunc("POST /api/routes", s.saveRoute)
	mux.HandleFunc("PUT /api/routes/{id}", s.saveRoute)
	mux.HandleFunc("DELETE /api/routes/{id}", s.deleteRoute)
	mux.HandleFunc("POST /api/routes/{id}/send", s.manualSend)
	mux.HandleFunc("GET /api/rules", s.listRules)
	mux.HandleFunc("POST /api/rules", s.saveRule)
	mux.HandleFunc("PUT /api/rules/{id}", s.saveRule)
	mux.HandleFunc("DELETE /api/rules/{id}", s.deleteRule)
	mux.HandleFunc("POST /api/simulate", s.simulate)
	mux.HandleFunc("GET /api/reviews", s.reviews)
	mux.HandleFunc("POST /api/reviews/{id}/action", s.reviewAction)
	mux.HandleFunc("GET /api/activity", s.activity)
	mux.HandleFunc("GET /api/outbox", s.outbox)
	mux.HandleFunc("POST /api/outbox/{id}/retry", s.retryOutbox)
	mux.HandleFunc("GET /api/settings", s.settings)
	mux.HandleFunc("PUT /api/settings", s.saveSettings)
	mux.HandleFunc("POST /api/ai/preview", s.aiPreview)
	mux.Handle("/", spaHandler(s.assets))
	return s.localHostOnly(s.securityHeaders(s.requestLog(mux)))
}

func (s *Server) dashboard(w http.ResponseWriter, _ *http.Request) {
	value, err := s.store.Dashboard()
	respond(w, value, err)
}
func (s *Server) accounts(w http.ResponseWriter, _ *http.Request) {
	value, err := s.store.ListAccounts()
	respond(w, value, err)
}
func (s *Server) routes(w http.ResponseWriter, _ *http.Request) {
	value, err := s.store.ListRoutes()
	respond(w, value, err)
}
func (s *Server) reviews(w http.ResponseWriter, r *http.Request) {
	value, err := s.store.ListReviews(r.URL.Query().Get("status"))
	respond(w, value, err)
}

func (s *Server) createAccount(w http.ResponseWriter, r *http.Request) {
	var input domain.AccountInput
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Name, input.Phone = strings.TrimSpace(input.Name), strings.TrimSpace(input.Phone)
	if input.Name == "" || input.Phone == "" || input.APIID <= 0 || len(input.APIHash) < 8 {
		writeError(w, http.StatusBadRequest, "名称、手机号、API ID 和 API Hash 均为必填项")
		return
	}
	encrypted, err := s.vault.Encrypt(input.APIHash)
	if err == nil {
		var account domain.Account
		account, err = s.store.SaveAccount(input, encrypted)
		if err == nil {
			s.record("info", "account", "已添加 Telegram 账号 "+account.Name, "")
			writeJSON(w, http.StatusCreated, account)
			return
		}
	}
	writeInternal(w, err)
}

func (s *Server) deleteAccount(w http.ResponseWriter, r *http.Request) {
	_ = s.runtime.Disconnect(r.PathValue("id"))
	err := s.store.DeleteAccount(r.PathValue("id"))
	if err == nil {
		s.record("info", "account", "已删除 Telegram 账号", "")
	}
	respondEmpty(w, err)
}
func (s *Server) connectAccount(w http.ResponseWriter, r *http.Request) {
	respondAccepted(w, s.runtime.Connect(r.PathValue("id")))
}
func (s *Server) disconnectAccount(w http.ResponseWriter, r *http.Request) {
	respondAccepted(w, s.runtime.Disconnect(r.PathValue("id")))
}

func (s *Server) submitCode(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Code string `json:"code"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	respondAccepted(w, s.runtime.SubmitCode(r.PathValue("id"), strings.TrimSpace(input.Code)))
}
func (s *Server) submitPassword(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	respondAccepted(w, s.runtime.SubmitPassword(r.PathValue("id"), input.Password))
}

func (s *Server) accountPeers(w http.ResponseWriter, r *http.Request) {
	value, err := s.runtime.ListPeers(r.PathValue("id"))
	respond(w, value, err)
}

func validateRoute(route *domain.Route) error {
	if strings.TrimSpace(route.Name) == "" {
		return errors.New("线路名称不能为空")
	}
	if route.AccountID == "" {
		return errors.New("请选择 Telegram 账号")
	}
	if len(route.Sources) == 0 || len(route.Targets) == 0 {
		return errors.New("至少需要一个来源和一个目标")
	}
	for _, source := range route.Sources {
		for _, target := range route.Targets {
			if source.ChatID == target.ChatID && source.TopicID == target.TopicID {
				return errors.New("来源和目标不能相同")
			}
		}
	}
	if route.Mode != "copy" && route.Mode != "forward" {
		route.Mode = "copy"
	}
	if route.ReviewMode == "" {
		route.ReviewMode = "rules"
	}
	if len(route.SenderAccountIDs) == 0 {
		route.SenderAccountIDs = []string{route.AccountID}
	}
	switch route.SenderFilterMode {
	case "all", "admins", "allowlist", "admins_and_allowlist":
	default:
		route.SenderFilterMode = "all"
	}
	switch route.ButtonPolicy {
	case "drop", "urls_only", "as_text":
	default:
		route.ButtonPolicy = "urls_only"
	}
	return nil
}

func (s *Server) saveRoute(w http.ResponseWriter, r *http.Request) {
	var route domain.Route
	if !decodeJSON(w, r, &route) {
		return
	}
	if id := r.PathValue("id"); id != "" {
		route.ID = id
	}
	if err := validateRoute(&route); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	saved, err := s.store.SaveRoute(route)
	if err == nil {
		s.record("info", "route", "已保存线路 "+saved.Name, saved.ID)
	}
	respond(w, saved, err)
}
func (s *Server) deleteRoute(w http.ResponseWriter, r *http.Request) {
	err := s.store.DeleteRoute(r.PathValue("id"))
	if err == nil {
		s.record("info", "route", "已删除镜像线路", r.PathValue("id"))
	}
	respondEmpty(w, err)
}

func (s *Server) manualSend(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Text string `json:"text"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if strings.TrimSpace(input.Text) == "" {
		writeError(w, http.StatusBadRequest, "消息内容不能为空")
		return
	}
	err := s.runtime.SendManual(r.PathValue("id"), input.Text)
	if err == nil {
		s.record("info", "manual", "已手工插入一条消息", r.PathValue("id"))
	}
	respondAccepted(w, err)
}

func (s *Server) listRules(w http.ResponseWriter, r *http.Request) {
	value, err := s.store.ListRules(r.URL.Query().Get("routeId"))
	respond(w, value, err)
}
func (s *Server) saveRule(w http.ResponseWriter, r *http.Request) {
	var rule domain.Rule
	if !decodeJSON(w, r, &rule) {
		return
	}
	if value := r.PathValue("id"); value != "" {
		rule.ID = value
	}
	if rule.RouteID == "" || strings.TrimSpace(rule.Name) == "" || rule.Kind == "" {
		writeError(w, http.StatusBadRequest, "线路、规则名称和类型为必填项")
		return
	}
	if rule.MessageType == "" {
		rule.MessageType = "all"
	}
	saved, err := s.store.SaveRule(rule)
	if err == nil {
		s.record("info", "rule", "已保存规则 "+saved.Name, saved.RouteID)
	}
	respond(w, saved, err)
}
func (s *Server) deleteRule(w http.ResponseWriter, r *http.Request) {
	respondEmpty(w, s.store.DeleteRule(r.PathValue("id")))
}

func (s *Server) simulate(w http.ResponseWriter, r *http.Request) {
	var message domain.MessageEnvelope
	if !decodeJSON(w, r, &message) {
		return
	}
	configured, err := s.store.ListRules(message.RouteID)
	if err != nil {
		writeInternal(w, err)
		return
	}
	result := s.engine.Apply(message, configured)
	if result.Decision == "review" {
		routes, _ := s.store.ListRoutes()
		routeName := ""
		for _, route := range routes {
			if route.ID == message.RouteID {
				routeName = route.Name
				break
			}
		}
		_, err = s.store.AddReview(domain.ReviewItem{RouteID: message.RouteID, RouteName: routeName, SourceChatID: message.SourceChatID, SenderName: message.SenderName, MessageType: message.MessageType, OriginalText: message.Text, FinalText: result.Text, Reason: "规则要求人工审核"})
		if err != nil {
			writeInternal(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) reviewAction(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Action    string `json:"action"`
		FinalText string `json:"finalText"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.Action != "approve" && input.Action != "reject" {
		writeError(w, http.StatusBadRequest, "不支持的审核动作")
		return
	}
	if input.Action == "approve" {
		if err := s.store.UpdateReview(r.PathValue("id"), "pending", input.FinalText); err != nil {
			writeInternal(w, err)
			return
		}
		if err := s.runtime.Approve(r.PathValue("id")); err != nil {
			writeInternal(w, err)
			return
		}
	}
	status := map[string]string{"approve": "approved", "reject": "rejected"}[input.Action]
	err := s.store.UpdateReview(r.PathValue("id"), status, input.FinalText)
	if err == nil {
		s.record("info", "review", "审核项已"+map[string]string{"approve": "批准", "reject": "拒绝"}[input.Action], "")
	}
	respondAccepted(w, err)
}

func (s *Server) activity(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	value, err := s.store.ListActivities(limit)
	respond(w, value, err)
}
func (s *Server) outbox(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	value, err := s.store.ListOutbox(r.URL.Query().Get("status"), limit)
	respond(w, value, err)
}
func (s *Server) retryOutbox(w http.ResponseWriter, r *http.Request) {
	err := s.store.RequeueOutbox(r.PathValue("id"))
	if err == nil {
		s.record("info", "queue", "失败任务已重新进入发送队列", "")
	}
	respondAccepted(w, err)
}
func (s *Server) settings(w http.ResponseWriter, _ *http.Request) {
	value, err := s.store.Settings()
	if err == nil {
		_, secretErr := s.store.Secret("ai_api_key")
		value.AI.HasAPIKey = secretErr == nil
		value.AI.APIKey = ""
	}
	respond(w, value, err)
}
func (s *Server) saveSettings(w http.ResponseWriter, r *http.Request) {
	var value domain.Settings
	if !decodeJSON(w, r, &value) {
		return
	}
	if value.RetentionDays < 1 || value.RetentionDays > 3650 || value.MediaCacheMB < 128 {
		writeError(w, http.StatusBadRequest, "保留天数或缓存容量无效")
		return
	}
	if err := validateLocalAddress(value.ListenAddress); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if value.AI.TimeoutSeconds < 1 || value.AI.TimeoutSeconds > 300 || value.AI.MaxInputChars < 100 || value.AI.MaxInputChars > 100000 {
		writeError(w, http.StatusBadRequest, "AI 超时或最大输入长度无效")
		return
	}
	if value.AI.FailurePolicy != "review" && value.AI.FailurePolicy != "send" {
		writeError(w, http.StatusBadRequest, "AI 失败策略无效")
		return
	}
	if value.AI.Enabled {
		parsed, err := url.Parse(value.AI.BaseURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || strings.TrimSpace(value.AI.Model) == "" {
			writeError(w, http.StatusBadRequest, "AI URL 或模型无效")
			return
		}
	}
	if err := startup.Configure(value.StartWithWindows); err != nil {
		writeInternal(w, err)
		return
	}
	if strings.TrimSpace(value.AI.APIKey) != "" {
		encrypted, err := s.vault.Encrypt(strings.TrimSpace(value.AI.APIKey))
		if err != nil {
			writeInternal(w, err)
			return
		}
		if err := s.store.SaveSecret("ai_api_key", encrypted); err != nil {
			writeInternal(w, err)
			return
		}
	}
	value.AI.APIKey = ""
	_, secretErr := s.store.Secret("ai_api_key")
	value.AI.HasAPIKey = secretErr == nil
	err := s.store.SaveSettings(value)
	respond(w, value, err)
}

func (s *Server) aiPreview(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Text        string `json:"text"`
		RoutePrompt string `json:"routePrompt"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	settings, err := s.store.Settings()
	if err != nil {
		writeInternal(w, err)
		return
	}
	encrypted, err := s.store.Secret("ai_api_key")
	if err != nil {
		writeError(w, http.StatusBadRequest, "尚未保存 AI API Key")
		return
	}
	key, err := s.vault.Decrypt(encrypted)
	if err != nil {
		writeInternal(w, err)
		return
	}
	result, err := aiprocessor.Process(r.Context(), settings.AI, key, input.RoutePrompt, input.Text)
	respond(w, result, err)
}

func validateLocalAddress(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return errors.New("监听地址格式无效，应类似 127.0.0.1:8765")
	}
	host = strings.Trim(host, "[]")
	ip := net.ParseIP(host)
	if !strings.EqualFold(host, "localhost") && (ip == nil || !ip.IsLoopback()) {
		return errors.New("监听地址必须是本机回环地址")
	}
	return nil
}

func (s *Server) record(level, category, message, routeID string) {
	if err := s.store.AddActivity(domain.Activity{Level: level, Category: category, Message: message, RouteID: routeID}); err != nil {
		s.log.Error("record activity", "error", err)
	}
}

func (s *Server) requestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		if strings.HasPrefix(r.URL.Path, "/api/") {
			s.log.Debug("http request", "method", r.Method, "path", r.URL.Path, "duration", time.Since(start))
		}
	})
}
func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) localHostOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if parsed, _, err := net.SplitHostPort(host); err == nil {
			host = parsed
		}
		host = strings.Trim(host, "[]")
		ip := net.ParseIP(host)
		if !strings.EqualFold(host, "localhost") && (ip == nil || !ip.IsLoopback()) {
			writeError(w, http.StatusForbidden, "仅允许从本机访问")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func spaHandler(assets fs.FS) http.Handler {
	files := http.FileServer(http.FS(assets))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" || path == "index.html" {
			serveIndex(w, assets)
			return
		}
		if _, err := fs.Stat(assets, path); err != nil {
			serveIndex(w, assets)
			return
		}
		r.URL.Path = "/" + path
		files.ServeHTTP(w, r)
	})
}

func serveIndex(w http.ResponseWriter, assets fs.FS) {
	content, err := fs.ReadFile(assets, "index.html")
	if err != nil {
		writeInternal(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(content)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(w, http.StatusBadRequest, "请求内容无效: "+err.Error())
		return false
	}
	return true
}
func respond(w http.ResponseWriter, value any, err error) {
	if err != nil {
		writeInternal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}
func respondEmpty(w http.ResponseWriter, err error) {
	if err != nil {
		writeInternal(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func respondAccepted(w http.ResponseWriter, err error) {
	if err != nil {
		writeInternal(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
func writeInternal(w http.ResponseWriter, err error) {
	writeError(w, http.StatusInternalServerError, fmt.Sprintf("操作失败: %v", err))
}
