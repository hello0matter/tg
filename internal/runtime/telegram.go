package runtime

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/telegram/dcs"
	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/telegram/message/styling"
	"github.com/gotd/td/tg"
	"golang.org/x/net/proxy"

	"tgworkbench/internal/aiprocessor"
	"tgworkbench/internal/connector"
	"tgworkbench/internal/domain"
	"tgworkbench/internal/rules"
	"tgworkbench/internal/store"
	"tgworkbench/internal/vault"
)

const (
	channelIDOffset                  int64 = 1_000_000_000_000
	telegramAPIHashSecret                  = "telegram_api_hash"
	telegramDialTimeout                    = 10 * time.Second
	telegramInitialConnectionTimeout       = 30 * time.Second
)

var (
	errAccountDisconnected      = errors.New("Telegram \u8fde\u63a5\u5df2\u53d6\u6d88")
	errInitialConnectionTimeout = errors.New("Telegram \u521d\u59cb\u8fde\u63a5\u8d85\u65f6\uff0c\u8bf7\u68c0\u67e5\u4ee3\u7406\u6216\u7f51\u7edc\u540e\u91cd\u8bd5")
)

type Manager struct {
	store       *store.Store
	vault       *vault.Vault
	dataDir     string
	log         *slog.Logger
	engine      rules.Engine
	queueCancel context.CancelFunc

	mu         sync.RWMutex
	sessions   map[string]*accountSession
	albumMu    sync.Mutex
	albums     map[string]*pendingAlbum
	adminMu    sync.Mutex
	admins     map[string]adminCache
	cooldown   map[string]time.Time
	roundRobin int
}

type adminCache struct {
	userIDs   map[int64]bool
	expiresAt time.Time
}

type accountSession struct {
	accountID string
	cancel    context.CancelCauseFunc
	auth      *webAuthenticator

	mu     sync.RWMutex
	client *telegram.Client
	peers  map[int64]tg.InputPeerClass
	info   map[int64]domain.PeerRef
}

type sourceMessage struct {
	chatID    int64
	messageID int
}

type pendingAlbum struct {
	session  *accountSession
	route    domain.Route
	messages []*tg.Message
	entities tg.Entities
	timer    *time.Timer
}

type webAuthenticator struct {
	accountID string
	phone     string
	store     *store.Store
	code      chan string
	password  chan string
	ready     chan struct{}
	readyOnce sync.Once
}

func NewManager(dataDir string, store *store.Store, vault *vault.Vault, log *slog.Logger) *Manager {
	queueCtx, queueCancel := context.WithCancel(context.Background())
	manager := &Manager{
		store:       store,
		vault:       vault,
		dataDir:     dataDir,
		log:         log,
		sessions:    make(map[string]*accountSession),
		albums:      make(map[string]*pendingAlbum),
		admins:      make(map[string]adminCache),
		cooldown:    make(map[string]time.Time),
		queueCancel: queueCancel,
	}
	for range 4 {
		go manager.outboxWorker(queueCtx)
	}
	return manager
}

func (m *Manager) Descriptor() connector.Descriptor {
	_, _, credentialErr := m.resolveTelegramCredentials(domain.Account{}, nil)
	return connector.Descriptor{
		Platform:                     connector.Telegram,
		Name:                         "Telegram 用户账号",
		Available:                    true,
		DefaultCredentialsConfigured: credentialErr == nil,
		Capabilities: connector.Capabilities{
			Text: true, Media: true, Albums: true, Edits: true, Deletes: true,
			Threads: true, URLButtons: true, UserSessions: true,
		},
		Credentials: []connector.CredentialField{
			{Key: "phone", Label: "手机号", Kind: "text", Required: true, Placeholder: "+86..."},
			{Key: "apiId", Label: "API ID", Kind: "number", Shared: true},
			{Key: "apiHash", Label: "API Hash", Kind: "password", Secret: true, Shared: true},
		},
	}
}

func (m *Manager) CreateAccount(input domain.AccountInput) (domain.Account, error) {
	input.Name = strings.TrimSpace(input.Name)
	if input.Phone == "" {
		input.Phone = input.ConnectorConfig["phone"]
	}
	if input.APIID <= 0 {
		input.APIID, _ = strconv.Atoi(input.ConnectorConfig["apiId"])
	}
	input.Phone = strings.TrimSpace(input.Phone)
	if input.Platform == "" {
		input.Platform = connector.Telegram
	}
	if input.Platform != connector.Telegram {
		return domain.Account{}, connector.InputError{Message: "Telegram Connector 不能创建其他平台账号"}
	}
	if input.APIHash == "" {
		input.APIHash = input.ConnectorSecrets["apiHash"]
	}
	input.APIHash = strings.TrimSpace(input.APIHash)
	if input.Name == "" || input.Phone == "" {
		return domain.Account{}, connector.InputError{Message: "账号备注和手机号均为必填项"}
	}
	hasOverrideID := input.APIID > 0
	hasOverrideHash := input.APIHash != ""
	if hasOverrideID != hasOverrideHash {
		return domain.Account{}, connector.InputError{Message: "独立 API ID 和 API Hash 必须同时填写"}
	}
	if input.APIID < 0 || (hasOverrideHash && len(input.APIHash) < 8) {
		return domain.Account{}, connector.InputError{Message: "独立 API ID 或 API Hash 无效"}
	}
	if input.ConnectorConfig == nil {
		input.ConnectorConfig = make(map[string]string)
	}
	input.ConnectorConfig["phone"] = input.Phone
	delete(input.ConnectorConfig, "apiId")
	var encrypted []byte
	if hasOverrideID {
		input.ConnectorConfig["apiId"] = strconv.Itoa(input.APIID)
		var err error
		encrypted, err = m.vault.Encrypt(input.APIHash)
		if err != nil {
			return domain.Account{}, err
		}
	} else {
		if _, _, err := m.resolveTelegramCredentials(domain.Account{}, nil); err != nil {
			return domain.Account{}, connector.InputError{Message: "请先在系统设置中配置 Telegram 客户端凭证，或为此账号填写独立 API 凭证"}
		}
	}
	return m.store.SaveAccount(input, encrypted)
}

func (m *Manager) Connect(accountID string) error {
	m.mu.Lock()
	if _, exists := m.sessions[accountID]; exists {
		m.mu.Unlock()
		return connector.InputError{Message: "\u8be5\u8d26\u53f7\u5df2\u5728\u8fde\u63a5\u6216\u767b\u5f55\u4e2d\uff0c\u8bf7\u7b49\u5f85\u6216\u5148\u53d6\u6d88\u5f53\u524d\u8fde\u63a5"}
	}
	account, encryptedHash, err := m.store.AccountCredentials(accountID)
	if err != nil {
		m.mu.Unlock()
		return fmt.Errorf("读取账号: %w", err)
	}
	apiID, apiHash, err := m.resolveTelegramCredentials(account, encryptedHash)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	account.APIID = apiID
	ctx, cancel := context.WithCancelCause(context.Background())
	s := &accountSession{
		accountID: accountID,
		cancel:    cancel,
		peers:     make(map[int64]tg.InputPeerClass),
		info:      make(map[int64]domain.PeerRef),
	}
	s.auth = &webAuthenticator{
		accountID: accountID,
		phone:     account.Phone,
		store:     m.store,
		code:      make(chan string),
		password:  make(chan string),
		ready:     make(chan struct{}),
	}
	m.sessions[accountID] = s
	m.mu.Unlock()

	if err := m.store.UpdateAccountStatus(accountID, "connecting", account.Username, "", account.UserID); err != nil {
		m.remove(accountID)
		return err
	}
	go m.run(ctx, account, apiHash, s)
	return nil
}

func (m *Manager) resolveTelegramCredentials(account domain.Account, encryptedAccountHash []byte) (int, string, error) {
	if account.APIID > 0 || len(encryptedAccountHash) > 0 {
		if account.APIID <= 0 || len(encryptedAccountHash) == 0 {
			return 0, "", errors.New("账号的独立 Telegram API 凭证不完整")
		}
		apiHash, err := m.vault.Decrypt(encryptedAccountHash)
		if err != nil {
			return 0, "", fmt.Errorf("解密账号 API Hash: %w", err)
		}
		return account.APIID, apiHash, nil
	}
	settings, err := m.store.Settings()
	if err != nil {
		return 0, "", fmt.Errorf("读取 Telegram 客户端凭证: %w", err)
	}
	encryptedHash, err := m.store.Secret(telegramAPIHashSecret)
	if err != nil || settings.Telegram.APIID <= 0 || len(encryptedHash) == 0 {
		return 0, "", errors.New("尚未配置可用的 Telegram 客户端凭证")
	}
	apiHash, err := m.vault.Decrypt(encryptedHash)
	if err != nil {
		return 0, "", fmt.Errorf("解密全局 Telegram API Hash: %w", err)
	}
	return settings.Telegram.APIID, apiHash, nil
}

func (m *Manager) run(ctx context.Context, account domain.Account, apiHash string, s *accountSession) {
	dispatcher := tg.NewUpdateDispatcher()
	dispatcher.OnNewMessage(func(ctx context.Context, entities tg.Entities, update *tg.UpdateNewMessage) error {
		return m.handleNewMessage(ctx, s, entities, update)
	})
	dispatcher.OnEditMessage(func(ctx context.Context, entities tg.Entities, update *tg.UpdateEditMessage) error {
		return m.handleEditedMessage(ctx, s, entities, update.Message)
	})
	dispatcher.OnEditChannelMessage(func(ctx context.Context, entities tg.Entities, update *tg.UpdateEditChannelMessage) error {
		return m.handleEditedMessage(ctx, s, entities, update.Message)
	})
	dispatcher.OnDeleteMessages(func(ctx context.Context, _ tg.Entities, update *tg.UpdateDeleteMessages) error {
		return m.handleDeletedMessages(ctx, s /*sourceChatID*/, 0, update.Messages)
	})
	dispatcher.OnDeleteChannelMessages(func(ctx context.Context, _ tg.Entities, update *tg.UpdateDeleteChannelMessages) error {
		return m.handleDeletedMessages(ctx, s, -channelIDOffset-update.ChannelID, update.Messages)
	})
	options := telegram.Options{
		DialTimeout: telegramDialTimeout,
		SessionStorage: &encryptedSessionStorage{
			path:  filepath.Join(m.dataDir, "sessions", account.ID+".session"),
			vault: m.vault,
		},
		UpdateHandler: dispatcher,
	}
	settings, settingsErr := m.store.Settings()
	if settingsErr != nil {
		m.failConnection(account, settingsErr)
		return
	}
	if strings.TrimSpace(settings.ProxyURL) != "" {
		resolver, err := telegramProxyResolver(settings.ProxyURL)
		if err != nil {
			m.failConnection(account, err)
			return
		}
		options.Resolver = resolver
	}
	client := telegram.NewClient(account.APIID, apiHash, options)
	s.mu.Lock()
	s.client = client
	s.mu.Unlock()

	watchdogDone := make(chan struct{})
	defer close(watchdogDone)
	go watchInitialTelegramConnection(ctx, s.auth.ready, watchdogDone, telegramInitialConnectionTimeout, s.cancel)

	err := client.Run(ctx, func(ctx context.Context) error {
		flow := auth.NewFlow(s.auth, auth.SendCodeOptions{})
		if err := client.Auth().IfNecessary(ctx, flow); err != nil {
			return err
		}
		s.auth.markReady()
		status, err := client.Auth().Status(ctx)
		if err != nil {
			return err
		}
		username := ""
		userID := int64(0)
		if status.User != nil {
			username, userID = status.User.Username, status.User.ID
		}
		if err := m.store.UpdateAccountStatus(account.ID, "connected", username, "", userID); err != nil {
			return err
		}
		m.activity("info", "account", "Telegram 账号已连接: "+account.Name, "")
		if err := m.loadDialogs(ctx, s); err != nil {
			m.activity("warning", "account", "加载对话列表失败: "+err.Error(), "")
		}
		<-ctx.Done()
		return ctx.Err()
	})

	m.remove(account.ID)
	cause := context.Cause(ctx)
	if errors.Is(cause, errAccountDisconnected) {
		_ = m.store.UpdateAccountStatus(account.ID, "disconnected", account.Username, "", account.UserID)
		return
	}
	if cause != nil {
		message := cause.Error()
		_ = m.store.UpdateAccountStatus(account.ID, "error", account.Username, message, account.UserID)
		m.activity("error", "account", "Telegram \u8fde\u63a5\u5931\u8d25: "+message, "")
		return
	}
	if err == nil {
		err = errors.New("Telegram \u8fde\u63a5\u610f\u5916\u7ed3\u675f")
	}
	message := err.Error()
	_ = m.store.UpdateAccountStatus(account.ID, "error", account.Username, message, account.UserID)
	m.activity("error", "account", "Telegram \u8fde\u63a5\u5931\u8d25: "+message, "")
}

func watchInitialTelegramConnection(ctx context.Context, ready, done <-chan struct{}, timeout time.Duration, cancel context.CancelCauseFunc) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-ready:
	case <-done:
	case <-ctx.Done():
	case <-timer.C:
		cancel(errInitialConnectionTimeout)
	}
}

func (m *Manager) failConnection(account domain.Account, err error) {
	m.remove(account.ID)
	_ = m.store.UpdateAccountStatus(account.ID, "error", account.Username, err.Error(), account.UserID)
	m.activity("error", "account", "Telegram 连接失败: "+err.Error(), "")
}

func telegramProxyResolver(rawURL string) (dcs.Resolver, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("代理地址无效: %w", err)
	}
	if u.Scheme != "socks5" && u.Scheme != "socks5h" {
		return nil, errors.New("Telegram 代理目前仅支持 socks5:// 或 socks5h://")
	}
	dialer, err := proxy.FromURL(u, proxy.Direct)
	if err != nil {
		return nil, fmt.Errorf("创建代理连接: %w", err)
	}
	dial := func(ctx context.Context, network, address string) (net.Conn, error) {
		if contextual, ok := dialer.(proxy.ContextDialer); ok {
			return contextual.DialContext(ctx, network, address)
		}
		type result struct {
			conn net.Conn
			err  error
		}
		completed := make(chan result, 1)
		go func() {
			conn, err := dialer.Dial(network, address)
			completed <- result{conn: conn, err: err}
		}()
		select {
		case value := <-completed:
			return value.conn, value.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return dcs.Plain(dcs.PlainOptions{Dial: dial}), nil
}

func (m *Manager) Disconnect(accountID string) error {
	m.mu.RLock()
	s, ok := m.sessions[accountID]
	m.mu.RUnlock()
	if !ok {
		return m.store.UpdateAccountStatus(accountID, "disconnected", "", "", 0)
	}
	s.cancel(errAccountDisconnected)
	return nil
}

func (m *Manager) SubmitCode(accountID, code string) error {
	s, err := m.session(accountID)
	if err != nil {
		return err
	}
	select {
	case s.auth.code <- code:
		return nil
	case <-time.After(5 * time.Second):
		return errors.New("当前登录流程未等待验证码")
	}
}

func (m *Manager) SubmitPassword(accountID, password string) error {
	s, err := m.session(accountID)
	if err != nil {
		return err
	}
	select {
	case s.auth.password <- password:
		return nil
	case <-time.After(5 * time.Second):
		return errors.New("当前登录流程未等待两步验证密码")
	}
}

func (m *Manager) Approve(reviewID string) error {
	item, err := m.store.Review(reviewID)
	if err != nil {
		return err
	}
	if item.Status != "pending" {
		return errors.New("该审核项已经处理")
	}
	return m.SendManual(item.RouteID, item.FinalText)
}

func (m *Manager) SendManual(routeID, text string) error {
	route, err := m.store.Route(routeID)
	if err != nil {
		return err
	}
	return m.enqueueText(route, text, nil, nil)
}

func (m *Manager) enqueueText(route domain.Route, text string, buttons []domain.ButtonLink, source *sourceMessage) error {
	if strings.TrimSpace(text) == "" {
		return errors.New("发送内容为空")
	}
	accountIDs := route.SenderAccountIDs
	if len(accountIDs) == 0 {
		accountIDs = []string{route.AccountID}
	}
	jobs := make([]domain.OutboxJob, 0, len(route.Targets))
	for _, target := range route.Targets {
		platform := target.Platform
		if platform == "" {
			platform = connector.Telegram
		}
		targetAccountIDs := accountIDs
		if target.ConnectorID != "" {
			targetAccountIDs = []string{target.ConnectorID}
		}
		job := domain.OutboxJob{
			RouteID: route.ID, RouteName: route.Name, Platform: platform, Target: target, Text: text, Buttons: buttons,
			SenderAccountIDs: targetAccountIDs,
			OrderKey:         fmt.Sprintf("%s:%d:%d", platform, target.ChatID, target.TopicID),
		}
		if source != nil {
			job.SourceChatID, job.SourceMessageID = source.chatID, source.messageID
			job.DedupeKey = fmt.Sprintf("%s:%d:%d:%d:%d:text", route.ID, source.chatID, source.messageID, target.ChatID, target.TopicID)
		}
		jobs = append(jobs, job)
	}
	if err := m.store.EnqueueOutbox(jobs); err != nil {
		return err
	}
	m.activity("info", "queue", fmt.Sprintf("消息已进入发送队列，共 %d 个目标", len(jobs)), route.ID)
	return nil
}

func (m *Manager) outboxWorker(ctx context.Context) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		job, err := m.store.ClaimOutbox(connector.Telegram, 2*time.Minute)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			m.log.Error("claim outbox", "error", err)
			continue
		}
		m.processOutbox(ctx, job)
	}
}

func (m *Manager) processOutbox(ctx context.Context, job domain.OutboxJob) {
	session, accountID := m.pickSender(job)
	if session == nil {
		_ = m.store.DeferOutbox(job.ID, "账号池暂无可写入该目标的在线账号", time.Now().Add(5*time.Second))
		return
	}
	sendCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	err := m.sendQueuedText(sendCtx, session, job)
	if err == nil {
		_ = m.store.CompleteOutbox(job.ID, accountID)
		m.activity("info", "sent", "已通过账号池同步到 "+displayPeer(job.Target), job.RouteID)
		return
	}
	delay := time.Duration(1<<min(job.Attempts, 8)) * time.Second
	if flood := floodWait(err); flood > 0 {
		delay = flood
		m.mu.Lock()
		m.cooldown[accountID] = time.Now().Add(flood)
		m.mu.Unlock()
	}
	final := job.Attempts >= 11
	_ = m.store.RetryOutbox(job.ID, accountID, err.Error(), time.Now().Add(delay), final)
	level := "warning"
	if final {
		level = "error"
	}
	m.activity(level, "queue", "队列发送失败，将按策略处理: "+err.Error(), job.RouteID)
}

func (m *Manager) pickSender(job domain.OutboxJob) (*accountSession, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(job.SenderAccountIDs) == 0 {
		return nil, ""
	}
	start := m.roundRobin % len(job.SenderAccountIDs)
	for offset := range len(job.SenderAccountIDs) {
		accountID := job.SenderAccountIDs[(start+offset)%len(job.SenderAccountIDs)]
		if until := m.cooldown[accountID]; until.After(time.Now()) {
			continue
		}
		session := m.sessions[accountID]
		if session == nil {
			continue
		}
		session.mu.RLock()
		_, hasPeer := session.peers[job.Target.ChatID]
		ready := session.client != nil
		session.mu.RUnlock()
		if ready && hasPeer {
			m.roundRobin = start + offset + 1
			return session, accountID
		}
	}
	return nil, ""
}

func (m *Manager) sendQueuedText(ctx context.Context, session *accountSession, job domain.OutboxJob) error {
	session.mu.RLock()
	client := session.client
	session.mu.RUnlock()
	if client == nil {
		return errors.New("Telegram 客户端尚未就绪")
	}
	peer, err := session.peer(job.Target.ChatID)
	if err != nil {
		return err
	}
	request := &tg.MessagesSendMessageRequest{Peer: peer, Message: job.Text, RandomID: job.RandomID}
	if job.Target.TopicID > 0 {
		request.ReplyTo = &tg.InputReplyToMessage{ReplyToMsgID: job.Target.TopicID}
	}
	updates, err := client.API().MessagesSendMessage(ctx, request)
	if err != nil {
		return err
	}
	if job.SourceMessageID != 0 {
		m.recordMapping(job.RouteID, &sourceMessage{chatID: job.SourceChatID, messageID: job.SourceMessageID}, job.Target.ChatID, sentMessageID(updates), session.accountID)
	}
	return nil
}

func floodWait(err error) time.Duration {
	message := strings.ToUpper(err.Error())
	marker := "FLOOD_WAIT_"
	index := strings.Index(message, marker)
	if index < 0 {
		return 0
	}
	digits := message[index+len(marker):]
	if end := strings.IndexFunc(digits, func(r rune) bool { return r < '0' || r > '9' }); end >= 0 {
		digits = digits[:end]
	}
	seconds, _ := strconv.Atoi(digits)
	if seconds <= 0 {
		return 0
	}
	return time.Duration(seconds+1) * time.Second
}

func (m *Manager) ListPeers(accountID string) ([]domain.PeerRef, error) {
	s, err := m.session(accountID)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	result := make([]domain.PeerRef, 0, len(s.info))
	for _, peer := range s.info {
		result = append(result, peer)
	}
	s.mu.RUnlock()
	sort.Slice(result, func(i, j int) bool {
		if result[i].Kind == result[j].Kind {
			return strings.ToLower(result[i].Title) < strings.ToLower(result[j].Title)
		}
		return result[i].Kind < result[j].Kind
	})
	return result, nil
}

func (m *Manager) Close() {
	m.queueCancel()
	m.mu.RLock()
	sessions := make([]*accountSession, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	m.mu.RUnlock()
	for _, s := range sessions {
		s.cancel(errAccountDisconnected)
	}
}

func (m *Manager) session(accountID string) (*accountSession, error) {
	m.mu.RLock()
	s, ok := m.sessions[accountID]
	m.mu.RUnlock()
	if !ok {
		return nil, errors.New("账号未连接")
	}
	return s, nil
}

func (m *Manager) remove(accountID string) {
	m.mu.Lock()
	delete(m.sessions, accountID)
	m.mu.Unlock()
}

func (m *Manager) loadDialogs(ctx context.Context, s *accountSession) error {
	s.mu.RLock()
	client := s.client
	s.mu.RUnlock()
	if client == nil {
		return errors.New("Telegram 客户端尚未就绪")
	}
	dialogs, err := client.API().MessagesGetDialogs(ctx, &tg.MessagesGetDialogsRequest{
		OffsetPeer: &tg.InputPeerEmpty{},
		Limit:      100,
	})
	if err != nil {
		return err
	}
	switch value := dialogs.(type) {
	case *tg.MessagesDialogs:
		s.cache(value.Users, value.Chats)
	case *tg.MessagesDialogsSlice:
		s.cache(value.Users, value.Chats)
	case *tg.MessagesDialogsNotModified:
		return nil
	default:
		return fmt.Errorf("不支持的对话响应 %T", dialogs)
	}
	return nil
}

func (s *accountSession) cache(users []tg.UserClass, chats []tg.ChatClass) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, raw := range users {
		if user, ok := raw.(*tg.User); ok {
			s.peers[user.ID] = &tg.InputPeerUser{UserID: user.ID, AccessHash: user.AccessHash}
			title := strings.TrimSpace(user.FirstName + " " + user.LastName)
			if title == "" {
				title = user.Username
			}
			s.info[user.ID] = domain.PeerRef{Platform: connector.Telegram, ConnectorID: s.accountID, ChatID: user.ID, Title: title, Kind: "user"}
		}
	}
	for _, raw := range chats {
		switch chat := raw.(type) {
		case *tg.Chat:
			s.peers[-chat.ID] = &tg.InputPeerChat{ChatID: chat.ID}
			s.info[-chat.ID] = domain.PeerRef{Platform: connector.Telegram, ConnectorID: s.accountID, ChatID: -chat.ID, Title: chat.Title, Kind: "group"}
		case *tg.Channel:
			id := -channelIDOffset - chat.ID
			s.peers[id] = &tg.InputPeerChannel{ChannelID: chat.ID, AccessHash: chat.AccessHash}
			kind := "channel"
			if chat.Megagroup {
				kind = "group"
			}
			s.info[id] = domain.PeerRef{Platform: connector.Telegram, ConnectorID: s.accountID, ChatID: id, Title: chat.Title, Kind: kind}
		}
	}
}

func (s *accountSession) cacheEntities(entities tg.Entities) {
	users := make([]tg.UserClass, 0, len(entities.Users))
	for _, user := range entities.Users {
		users = append(users, user)
	}
	chats := make([]tg.ChatClass, 0, len(entities.Chats)+len(entities.Channels))
	for _, chat := range entities.Chats {
		chats = append(chats, chat)
	}
	for _, channel := range entities.Channels {
		chats = append(chats, channel)
	}
	s.cache(users, chats)
}

func (m *Manager) handleNewMessage(ctx context.Context, s *accountSession, entities tg.Entities, update *tg.UpdateNewMessage) error {
	msg, ok := update.Message.(*tg.Message)
	if !ok || msg.Out {
		return nil
	}
	s.cacheEntities(entities)
	chatID := peerID(msg.PeerID)
	if chatID == 0 {
		return nil
	}
	topicID := messageTopic(msg)
	routes, err := m.store.ListRoutes()
	if err != nil {
		return err
	}
	for _, route := range routes {
		if !route.Enabled || route.AccountID != s.accountID || !matchesSource(route.Sources, chatID, topicID) {
			continue
		}
		allowed, err := m.senderAllowed(ctx, s, route, msg, chatID, entities)
		if err != nil {
			m.activity("warning", "filter", "无法刷新管理员列表，已跳过消息: "+err.Error(), route.ID)
			continue
		}
		if !allowed {
			m.activity("info", "filter", "已忽略非指定发送者的消息", route.ID)
			continue
		}
		if msg.Noforwards {
			m.activity("warning", "protected", "已跳过 Telegram 受保护内容", route.ID)
			continue
		}
		if msg.GroupedID != 0 {
			m.queueAlbum(s, route, msg, entities)
			continue
		}
		if err := m.processRoute(ctx, s, route, msg, chatID, topicID, entities); err != nil {
			m.activity("error", "send", "线路发送失败: "+err.Error(), route.ID)
		}
	}
	return nil
}

func (m *Manager) senderAllowed(ctx context.Context, session *accountSession, route domain.Route, msg *tg.Message, chatID int64, entities tg.Entities) (bool, error) {
	mode := route.SenderFilterMode
	if mode == "" || mode == "all" {
		return true, nil
	}
	senderID := peerID(msg.FromID)
	for _, allowedID := range route.AllowedSenderIDs {
		if allowedID == senderID {
			return true, nil
		}
	}
	if route.IncludeBots && senderID > 0 {
		if user := entities.Users[senderID]; user != nil && user.Bot {
			return true, nil
		}
	}
	if mode == "allowlist" {
		return false, nil
	}
	if msg.Post || senderID == chatID {
		return true, nil
	}
	admins, err := m.adminIDs(ctx, session, chatID)
	if err != nil {
		return false, err
	}
	return admins[senderID], nil
}

func (m *Manager) adminIDs(ctx context.Context, session *accountSession, chatID int64) (map[int64]bool, error) {
	key := session.accountID + ":" + strconv.FormatInt(chatID, 10)
	m.adminMu.Lock()
	if cached, ok := m.admins[key]; ok && cached.expiresAt.After(time.Now()) {
		m.adminMu.Unlock()
		return cached.userIDs, nil
	}
	m.adminMu.Unlock()

	session.mu.RLock()
	client := session.client
	peer := session.peers[chatID]
	session.mu.RUnlock()
	if client == nil || peer == nil {
		return nil, errors.New("来源会话尚未加载")
	}
	result := make(map[int64]bool)
	switch input := peer.(type) {
	case *tg.InputPeerChannel:
		participants, err := client.API().ChannelsGetParticipants(ctx, &tg.ChannelsGetParticipantsRequest{
			Channel: &tg.InputChannel{ChannelID: input.ChannelID, AccessHash: input.AccessHash},
			Filter:  &tg.ChannelParticipantsAdmins{}, Limit: 200,
		})
		if err != nil {
			return nil, err
		}
		if listed, ok := participants.(*tg.ChannelsChannelParticipants); ok {
			for _, participant := range listed.Participants {
				if identified, ok := participant.(interface{ GetUserID() int64 }); ok {
					result[identified.GetUserID()] = true
				}
			}
		}
	case *tg.InputPeerChat:
		full, err := client.API().MessagesGetFullChat(ctx, input.ChatID)
		if err != nil {
			return nil, err
		}
		chat, ok := full.FullChat.(*tg.ChatFull)
		if !ok {
			break
		}
		participants, ok := chat.Participants.(*tg.ChatParticipants)
		if !ok {
			break
		}
		for _, participant := range participants.Participants {
			switch value := participant.(type) {
			case *tg.ChatParticipantAdmin:
				result[value.UserID] = true
			case *tg.ChatParticipantCreator:
				result[value.UserID] = true
			}
		}
	default:
		return result, nil
	}
	m.adminMu.Lock()
	m.admins[key] = adminCache{userIDs: result, expiresAt: time.Now().Add(10 * time.Minute)}
	m.adminMu.Unlock()
	return result, nil
}

func (m *Manager) queueAlbum(s *accountSession, route domain.Route, msg *tg.Message, entities tg.Entities) {
	key := fmt.Sprintf("%s:%s:%d:%d", s.accountID, route.ID, peerID(msg.PeerID), msg.GroupedID)
	m.albumMu.Lock()
	if pending, ok := m.albums[key]; ok {
		pending.messages = append(pending.messages, msg)
		pending.entities = entities
		pending.timer.Reset(800 * time.Millisecond)
		m.albumMu.Unlock()
		return
	}
	pending := &pendingAlbum{session: s, route: route, messages: []*tg.Message{msg}, entities: entities}
	pending.timer = time.AfterFunc(800*time.Millisecond, func() { m.flushAlbum(key) })
	m.albums[key] = pending
	m.albumMu.Unlock()
}

func (m *Manager) flushAlbum(key string) {
	m.albumMu.Lock()
	pending, ok := m.albums[key]
	if ok {
		delete(m.albums, key)
	}
	m.albumMu.Unlock()
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := m.processAlbum(ctx, pending); err != nil {
		m.activity("error", "album", "相册同步失败: "+err.Error(), pending.route.ID)
	}
}

func (m *Manager) processAlbum(ctx context.Context, pending *pendingAlbum) error {
	sort.Slice(pending.messages, func(i, j int) bool { return pending.messages[i].ID < pending.messages[j].ID })
	configured, err := m.store.ListRules(pending.route.ID)
	if err != nil {
		return err
	}
	captions := make([]string, len(pending.messages))
	original := make([]string, len(pending.messages))
	decision := "send"
	for i, msg := range pending.messages {
		original[i] = msg.Message
		result := m.engine.Apply(domain.MessageEnvelope{
			Platform: connector.Telegram, AccountID: pending.session.accountID,
			RouteID: pending.route.ID, SourceChatID: peerID(msg.PeerID), SourceMsgID: msg.ID,
			TopicID: messageTopic(msg), SenderName: senderName(msg.FromID, pending.entities),
			MessageType: classifyMessage(msg), Caption: msg.Message, ReplyToID: replyMessageID(msg),
			ThreadKey: threadKey(peerID(msg.PeerID), messageTopic(msg)), Buttons: normalizedButtons(msg.ReplyMarkup),
			Attachments: normalizedAttachments(msg),
		}, configured)
		if pending.route.AIEnabled && result.Decision == "send" && strings.TrimSpace(result.Caption) != "" {
			aiResult, _, err := m.applyAI(ctx, pending.route, result.Caption)
			if err != nil {
				return err
			}
			result.Caption = aiResult.Text
			result.Decision = aiResult.Decision
		}
		if suffix := formatButtons(msg.ReplyMarkup, pending.route.ButtonPolicy); suffix != "" {
			result.Caption = strings.TrimSpace(result.Caption + "\n\n" + suffix)
		}
		captions[i] = result.Caption
		if result.Decision == "review" {
			decision = "review"
		} else if result.Decision == "drop" && decision == "send" {
			decision = "drop"
		}
	}
	if pending.route.ReviewMode == "all" {
		decision = "review"
	}
	if decision == "drop" {
		m.activity("info", "drop", "相册命中丢弃规则", pending.route.ID)
		return nil
	}
	if decision == "review" {
		first := pending.messages[0]
		_, err := m.store.AddReview(domain.ReviewItem{
			RouteID: pending.route.ID, RouteName: pending.route.Name,
			SourceChatID: peerID(first.PeerID), SourceTitle: sourceTitle(pending.route.Sources, peerID(first.PeerID), messageTopic(first)),
			SenderName: senderName(first.FromID, pending.entities), MessageType: "album",
			OriginalText: strings.Join(original, "\n"), FinalText: strings.Join(captions, "\n"),
			Reason: "相册中的消息命中审核策略，批准后将以合并文本发送",
		})
		return err
	}
	if pending.route.Mode == "forward" && stringSlicesEqual(original, captions) && !albumHasButtons(pending.messages) {
		return m.forwardAlbum(ctx, pending)
	}
	return m.copyAlbum(ctx, pending, captions)
}

func (m *Manager) copyAlbum(ctx context.Context, pending *pendingAlbum, captions []string) error {
	options := make([]message.MultiMediaOption, len(pending.messages))
	for i, msg := range pending.messages {
		option, err := reusableMedia(msg.Media, captions[i])
		if err != nil {
			return err
		}
		options[i] = message.ForceMulti(option)
	}
	if len(options) == 1 {
		return m.sendMedia(ctx, pending.session, pending.route, pending.messages[0].Media, captions[0], &sourceMessage{chatID: peerID(pending.messages[0].PeerID), messageID: pending.messages[0].ID})
	}
	pending.session.mu.RLock()
	client := pending.session.client
	pending.session.mu.RUnlock()
	if client == nil {
		return errors.New("Telegram 客户端尚未就绪")
	}
	sender := message.NewSender(client.API())
	for _, target := range pending.route.Targets {
		peer, err := pending.session.peer(target.ChatID)
		if err != nil {
			return err
		}
		builder := sender.To(peer)
		var send *message.Builder
		if target.TopicID > 0 {
			send = builder.Reply(target.TopicID)
		} else {
			send = &builder.Builder
		}
		updates, err := send.Album(ctx, options[0], options[1:]...)
		if err != nil {
			return err
		}
		m.recordAlbumMappings(pending.route.ID, pending.messages, target.ChatID, sentMessageIDs(updates))
		m.activity("info", "sent", "已复制相册到 "+displayPeer(target), pending.route.ID)
	}
	return nil
}

func (m *Manager) forwardAlbum(ctx context.Context, pending *pendingAlbum) error {
	first := pending.messages[0]
	from, err := pending.session.peer(peerID(first.PeerID))
	if err != nil {
		return err
	}
	ids := make([]int, len(pending.messages))
	for i, msg := range pending.messages {
		ids[i] = msg.ID
	}
	pending.session.mu.RLock()
	client := pending.session.client
	pending.session.mu.RUnlock()
	if client == nil {
		return errors.New("Telegram 客户端尚未就绪")
	}
	sender := message.NewSender(client.API())
	for _, target := range pending.route.Targets {
		peer, err := pending.session.peer(target.ChatID)
		if err != nil {
			return err
		}
		builder := sender.To(peer)
		var send *message.Builder
		if target.TopicID > 0 {
			send = builder.Reply(target.TopicID)
		} else {
			send = &builder.Builder
		}
		updates, err := send.ForwardIDs(from, ids[0], ids[1:]...).Send(ctx)
		if err != nil {
			return err
		}
		m.recordAlbumMappings(pending.route.ID, pending.messages, target.ChatID, sentMessageIDs(updates))
		m.activity("info", "sent", "已转发相册到 "+displayPeer(target), pending.route.ID)
	}
	return nil
}

func (m *Manager) processRoute(ctx context.Context, s *accountSession, route domain.Route, msg *tg.Message, chatID int64, topicID int, entities tg.Entities) error {
	messageType := classifyMessage(msg)
	senderID := peerID(msg.FromID)
	envelope := domain.MessageEnvelope{
		Platform:     connector.Telegram,
		AccountID:    s.accountID,
		RouteID:      route.ID,
		SourceChatID: chatID,
		SourceMsgID:  msg.ID,
		TopicID:      topicID,
		SenderID:     senderID,
		SenderName:   senderName(msg.FromID, entities),
		MessageType:  messageType,
		ReplyToID:    replyMessageID(msg),
		ThreadKey:    threadKey(chatID, topicID),
		Buttons:      normalizedButtons(msg.ReplyMarkup),
		Attachments:  normalizedAttachments(msg),
	}
	if messageType == "text" {
		envelope.Text = msg.Message
	} else {
		envelope.Caption = msg.Message
	}
	configured, err := m.store.ListRules(route.ID)
	if err != nil {
		return err
	}
	result := m.engine.Apply(envelope, configured)
	if result.Decision == "drop" {
		m.activity("info", "drop", "消息命中丢弃规则", route.ID)
		return nil
	}
	reason := "规则要求人工审核"
	if route.AIEnabled && result.Decision == "send" && strings.TrimSpace(firstNonEmpty(result.Text, result.Caption)) != "" {
		aiText := firstNonEmpty(result.Text, result.Caption)
		aiResult, aiReason, err := m.applyAI(ctx, route, aiText)
		if err != nil {
			return err
		}
		if messageType == "text" {
			result.Text = aiResult.Text
		} else {
			result.Caption = aiResult.Text
		}
		result.Decision = aiResult.Decision
		if aiReason != "" {
			reason = aiReason
		}
	}
	buttonSuffix := formatButtons(msg.ReplyMarkup, route.ButtonPolicy)
	if buttonSuffix != "" {
		if messageType == "text" {
			result.Text = strings.TrimSpace(result.Text + "\n\n" + buttonSuffix)
		} else {
			result.Caption = strings.TrimSpace(result.Caption + "\n\n" + buttonSuffix)
		}
	}
	if route.ReviewMode == "all" {
		result.Decision, reason = "review", "线路设置为全部审核"
	}
	if result.Decision == "drop" {
		m.activity("info", "ai", "消息被 AI 判定为丢弃", route.ID)
		return nil
	}
	if result.Decision == "review" {
		sourceTitle := sourceTitle(route.Sources, chatID, topicID)
		_, err := m.store.AddReview(domain.ReviewItem{
			RouteID:      route.ID,
			RouteName:    route.Name,
			SourceChatID: chatID,
			SourceTitle:  sourceTitle,
			SenderName:   envelope.SenderName,
			MessageType:  messageType,
			OriginalText: msg.Message,
			FinalText:    firstNonEmpty(result.Text, result.Caption),
			Reason:       reason,
		})
		if err == nil {
			m.activity("info", "review", "消息已进入审核队列", route.ID)
		}
		return err
	}
	if messageType != "text" {
		if route.Mode == "forward" && result.Caption == msg.Message && msg.ReplyMarkup == nil {
			return m.forward(ctx, s, route, chatID, msg.ID)
		}
		source := &sourceMessage{chatID: chatID, messageID: msg.ID}
		if err := m.sendMedia(ctx, s, route, msg.Media, result.Caption, source); err == nil {
			return nil
		} else if !errors.Is(err, errUnsupportedMedia) {
			return err
		}
		_, err := m.store.AddReview(domain.ReviewItem{
			RouteID: route.ID, RouteName: route.Name, SourceChatID: chatID,
			SourceTitle: sourceTitle(route.Sources, chatID, topicID), SenderName: envelope.SenderName,
			MessageType: messageType, OriginalText: msg.Message, FinalText: result.Caption,
			Reason: "该媒体类型无法在复制模式中保持原语义，可改稿后以文本发送",
		})
		return err
	}
	return m.enqueueText(route, result.Text, nil, &sourceMessage{chatID: chatID, messageID: msg.ID})
}

func (m *Manager) applyAI(ctx context.Context, route domain.Route, text string) (aiprocessor.Result, string, error) {
	settings, err := m.store.Settings()
	if err != nil {
		return aiprocessor.Result{}, "", err
	}
	if !settings.AI.Enabled {
		return aiprocessor.Result{Decision: "send", Text: text}, "", nil
	}
	encrypted, err := m.store.Secret("ai_api_key")
	if err == nil {
		var key string
		key, err = m.vault.Decrypt(encrypted)
		if err == nil {
			var result aiprocessor.Result
			result, err = aiprocessor.Process(ctx, settings.AI, key, route.AIPrompt, text)
			if err == nil {
				reason := strings.TrimSpace(result.Reason)
				if reason == "" && result.Decision == "review" {
					reason = "AI 要求人工审核"
				}
				return result, reason, nil
			}
		}
	}
	m.activity("warning", "ai", "AI 处理失败: "+err.Error(), route.ID)
	if settings.AI.FailurePolicy == "send" {
		return aiprocessor.Result{Decision: "send", Text: text}, "AI 失败后按配置原文发送", nil
	}
	return aiprocessor.Result{Decision: "review", Text: text}, "AI 处理失败，已转人工审核", nil
}

func formatButtons(markup tg.ReplyMarkupClass, policy string) string {
	if markup == nil || policy == "drop" {
		return ""
	}
	lines := make([]string, 0)
	for _, button := range normalizedButtons(markup) {
		if button.URL != "" {
			lines = append(lines, strings.TrimSpace(button.Text+" "+button.URL))
		} else if policy == "as_text" && button.Text != "" {
			lines = append(lines, "[按钮] "+button.Text)
		}
	}
	return strings.Join(lines, "\n")
}

func normalizedButtons(markup tg.ReplyMarkupClass) []domain.ButtonLink {
	rows, ok := markup.(interface{ GetRows() []tg.KeyboardButtonRow })
	if !ok {
		return nil
	}
	result := make([]domain.ButtonLink, 0)
	for _, row := range rows.GetRows() {
		for _, raw := range row.Buttons {
			button := domain.ButtonLink{}
			if named, ok := raw.(interface{ GetText() string }); ok {
				button.Text = strings.TrimSpace(named.GetText())
			}
			switch value := raw.(type) {
			case *tg.KeyboardButtonURL:
				button.URL = value.URL
			case *tg.KeyboardButtonWebView:
				button.URL = value.URL
			case *tg.KeyboardButtonSimpleWebView:
				button.URL = value.URL
			}
			if button.Text != "" || button.URL != "" {
				result = append(result, button)
			}
		}
	}
	return result
}

func normalizedAttachments(msg *tg.Message) []domain.Attachment {
	messageType := classifyMessage(msg)
	if messageType == "text" {
		return nil
	}
	attachment := domain.Attachment{Kind: messageType}
	switch media := msg.Media.(type) {
	case *tg.MessageMediaPhoto:
		if photo, ok := media.Photo.(*tg.Photo); ok {
			attachment.Ref = strconv.FormatInt(photo.ID, 10)
		}
	case *tg.MessageMediaDocument:
		if document, ok := media.Document.(*tg.Document); ok {
			attachment.Ref = strconv.FormatInt(document.ID, 10)
			attachment.MimeType = document.MimeType
			attachment.Size = document.Size
			for _, attribute := range document.Attributes {
				if filename, ok := attribute.(*tg.DocumentAttributeFilename); ok {
					attachment.FileName = filename.FileName
					break
				}
			}
		}
	}
	return []domain.Attachment{attachment}
}

func replyMessageID(msg *tg.Message) string {
	header, ok := msg.ReplyTo.(*tg.MessageReplyHeader)
	if !ok || header.ReplyToMsgID == 0 {
		return ""
	}
	return strconv.Itoa(header.ReplyToMsgID)
}

func threadKey(chatID int64, topicID int) string {
	return fmt.Sprintf("%s:%d:%d", connector.Telegram, chatID, topicID)
}

func albumHasButtons(messages []*tg.Message) bool {
	for _, msg := range messages {
		if msg.ReplyMarkup != nil {
			return true
		}
	}
	return false
}

func (m *Manager) handleEditedMessage(ctx context.Context, s *accountSession, entities tg.Entities, raw tg.MessageClass) error {
	msg, ok := raw.(*tg.Message)
	if !ok || msg.Out {
		return nil
	}
	s.cacheEntities(entities)
	chatID := peerID(msg.PeerID)
	mappings, err := m.store.MessageMappings(chatID, msg.ID)
	if err != nil {
		return err
	}
	for _, mapping := range mappings {
		route, err := m.store.Route(mapping.RouteID)
		if err != nil || !route.Enabled || !route.SyncEdits || route.AccountID != s.accountID {
			continue
		}
		messageType := classifyMessage(msg)
		envelope := domain.MessageEnvelope{
			Platform: connector.Telegram, AccountID: s.accountID,
			RouteID: route.ID, SourceChatID: chatID, SourceMsgID: msg.ID,
			TopicID: messageTopic(msg), SenderName: senderName(msg.FromID, entities),
			MessageType: messageType, ReplyToID: replyMessageID(msg),
			ThreadKey: threadKey(chatID, messageTopic(msg)), Buttons: normalizedButtons(msg.ReplyMarkup),
			Attachments: normalizedAttachments(msg),
		}
		if messageType == "text" {
			envelope.Text = msg.Message
		} else {
			envelope.Caption = msg.Message
		}
		configured, err := m.store.ListRules(route.ID)
		if err != nil {
			return err
		}
		result := m.engine.Apply(envelope, configured)
		if route.AIEnabled && result.Decision == "send" {
			aiText := firstNonEmpty(result.Text, result.Caption)
			if strings.TrimSpace(aiText) != "" {
				aiResult, _, err := m.applyAI(ctx, route, aiText)
				if err != nil {
					return err
				}
				if messageType == "text" {
					result.Text = aiResult.Text
				} else {
					result.Caption = aiResult.Text
				}
				result.Decision = aiResult.Decision
			}
		}
		if suffix := formatButtons(msg.ReplyMarkup, route.ButtonPolicy); suffix != "" {
			if messageType == "text" {
				result.Text = strings.TrimSpace(result.Text + "\n\n" + suffix)
			} else {
				result.Caption = strings.TrimSpace(result.Caption + "\n\n" + suffix)
			}
		}
		if result.Decision != "send" {
			m.activity("warning", "edit", "编辑后的消息命中审核或丢弃规则，目标未自动修改", route.ID)
			continue
		}
		senderSession := s
		if mapping.SenderAccountID != "" && mapping.SenderAccountID != s.accountID {
			if pooled, sessionErr := m.session(mapping.SenderAccountID); sessionErr == nil {
				senderSession = pooled
			}
		}
		if err := m.editMappedMessage(ctx, senderSession, mapping, msg.Media, firstNonEmpty(result.Text, result.Caption), messageType); err != nil {
			m.activity("error", "edit", "同步编辑失败: "+err.Error(), route.ID)
			continue
		}
		m.activity("info", "edit", "已同步编辑到目标消息", route.ID)
	}
	return nil
}

func (m *Manager) editMappedMessage(ctx context.Context, s *accountSession, mapping domain.MessageMapping, media tg.MessageMediaClass, text, messageType string) error {
	s.mu.RLock()
	client := s.client
	s.mu.RUnlock()
	if client == nil {
		return errors.New("Telegram 客户端尚未就绪")
	}
	peer, err := s.peer(mapping.TargetChatID)
	if err != nil {
		return err
	}
	edit := message.NewSender(client.API()).To(peer).Edit(mapping.TargetMessageID)
	if messageType == "text" {
		_, err = edit.Text(ctx, text)
		return err
	}
	option, err := reusableMedia(media, text)
	if err != nil {
		return err
	}
	_, err = edit.Media(ctx, option)
	return err
}

func (m *Manager) handleDeletedMessages(ctx context.Context, s *accountSession, sourceChatID int64, messageIDs []int) error {
	for _, messageID := range messageIDs {
		mappings, err := m.store.MessageMappings(sourceChatID, messageID)
		if err != nil {
			return err
		}
		for _, mapping := range mappings {
			route, err := m.store.Route(mapping.RouteID)
			if err != nil || !route.SyncDeletes || route.AccountID != s.accountID {
				continue
			}
			senderSession := s
			if mapping.SenderAccountID != "" && mapping.SenderAccountID != s.accountID {
				if pooled, sessionErr := m.session(mapping.SenderAccountID); sessionErr == nil {
					senderSession = pooled
				}
			}
			senderSession.mu.RLock()
			client := senderSession.client
			senderSession.mu.RUnlock()
			if client == nil {
				return errors.New("Telegram 客户端尚未就绪")
			}
			peer, err := senderSession.peer(mapping.TargetChatID)
			if err != nil {
				m.activity("error", "delete", "同步删除失败: "+err.Error(), route.ID)
				continue
			}
			if _, err := message.NewSender(client.API()).To(peer).Revoke().Messages(ctx, mapping.TargetMessageID); err != nil {
				m.activity("error", "delete", "同步删除失败: "+err.Error(), route.ID)
				continue
			}
			_ = m.store.DeleteMessageMapping(mapping)
			m.activity("info", "delete", "已同步删除目标消息", route.ID)
		}
	}
	return nil
}

func (m *Manager) recordMapping(routeID string, source *sourceMessage, targetChatID int64, targetMessageID int, senderAccountIDs ...string) {
	if source == nil || targetMessageID == 0 {
		return
	}
	senderAccountID := ""
	if len(senderAccountIDs) > 0 {
		senderAccountID = senderAccountIDs[0]
	}
	err := m.store.SaveMessageMapping(domain.MessageMapping{
		RouteID: routeID, SourceChatID: source.chatID, SourceMessageID: source.messageID,
		TargetChatID: targetChatID, TargetMessageID: targetMessageID, SenderAccountID: senderAccountID,
	})
	if err != nil {
		m.log.Error("save message mapping", "error", err)
	}
}

func (m *Manager) recordAlbumMappings(routeID string, sources []*tg.Message, targetChatID int64, targetMessageIDs []int) {
	if len(sources) != len(targetMessageIDs) {
		m.log.Warn("album message mapping incomplete", "sources", len(sources), "targets", len(targetMessageIDs))
		return
	}
	for i, source := range sources {
		m.recordMapping(routeID, &sourceMessage{chatID: peerID(source.PeerID), messageID: source.ID}, targetChatID, targetMessageIDs[i])
	}
}

func sentMessageID(updates tg.UpdatesClass) int {
	ids := sentMessageIDs(updates)
	if len(ids) > 0 {
		return ids[0]
	}
	return 0
}

func sentMessageIDs(updates tg.UpdatesClass) []int {
	switch value := updates.(type) {
	case *tg.Updates:
		return sentMessageIDsFromUpdates(value.Updates)
	case *tg.UpdatesCombined:
		return sentMessageIDsFromUpdates(value.Updates)
	case *tg.UpdateShortSentMessage:
		return []int{value.ID}
	default:
		return nil
	}
}

func sentMessageIDsFromUpdates(updates []tg.UpdateClass) []int {
	result := make([]int, 0)
	for _, update := range updates {
		if mapped, ok := update.(*tg.UpdateMessageID); ok {
			result = append(result, mapped.ID)
		}
	}
	if len(result) > 0 {
		sort.Ints(result)
		return result
	}
	for _, update := range updates {
		switch value := update.(type) {
		case *tg.UpdateNewMessage:
			result = append(result, value.Message.GetID())
		case *tg.UpdateNewChannelMessage:
			result = append(result, value.Message.GetID())
		}
	}
	sort.Ints(result)
	return result
}

var errUnsupportedMedia = errors.New("unsupported media type")

func (m *Manager) sendMedia(ctx context.Context, s *accountSession, route domain.Route, media tg.MessageMediaClass, caption string, source *sourceMessage) error {
	option, err := reusableMedia(media, caption)
	if err != nil {
		return err
	}
	s.mu.RLock()
	client := s.client
	s.mu.RUnlock()
	if client == nil {
		return errors.New("Telegram 客户端尚未就绪")
	}
	sender := message.NewSender(client.API())
	for _, target := range route.Targets {
		peer, err := s.peer(target.ChatID)
		if err != nil {
			return err
		}
		builder := sender.To(peer)
		var send *message.Builder
		if target.TopicID > 0 {
			send = builder.Reply(target.TopicID)
		} else {
			send = &builder.Builder
		}
		updates, err := send.Media(ctx, option)
		if err != nil {
			return fmt.Errorf("复制媒体到 %s: %w", target.Title, err)
		}
		m.recordMapping(route.ID, source, target.ChatID, sentMessageID(updates))
		m.activity("info", "sent", "已复制媒体到 "+displayPeer(target), route.ID)
	}
	return nil
}

func reusableMedia(media tg.MessageMediaClass, caption string) (message.MediaOption, error) {
	text := styling.Plain(caption)
	switch value := media.(type) {
	case *tg.MessageMediaPhoto:
		photo, ok := value.Photo.(*tg.Photo)
		if !ok {
			return nil, errUnsupportedMedia
		}
		return message.Photo(photo, text).Spoiler(value.Spoiler), nil
	case *tg.MessageMediaDocument:
		document, ok := value.Document.(*tg.Document)
		if !ok {
			return nil, errUnsupportedMedia
		}
		return message.Document(document, text).Spoiler(value.Spoiler), nil
	case *tg.MessageMediaContact:
		input := new(tg.InputMediaContact)
		input.FillFrom(value)
		return message.Media(input, text), nil
	case *tg.MessageMediaGeo:
		geo, ok := value.Geo.(*tg.GeoPoint)
		if !ok {
			return nil, errUnsupportedMedia
		}
		input := new(tg.InputGeoPoint)
		input.FillFrom(geo)
		return message.Media(&tg.InputMediaGeoPoint{GeoPoint: input}, text), nil
	case *tg.MessageMediaVenue:
		geo, ok := value.Geo.(*tg.GeoPoint)
		if !ok {
			return nil, errUnsupportedMedia
		}
		input := new(tg.InputGeoPoint)
		input.FillFrom(geo)
		return message.Media(&tg.InputMediaVenue{
			GeoPoint: input, Title: value.Title, Address: value.Address,
			Provider: value.Provider, VenueID: value.VenueID, VenueType: value.VenueType,
		}, text), nil
	case *tg.MessageMediaDice:
		input := new(tg.InputMediaDice)
		input.FillFrom(value)
		return message.Media(input, text), nil
	default:
		return nil, errUnsupportedMedia
	}
}

func (m *Manager) sendText(ctx context.Context, s *accountSession, route domain.Route, text string, source *sourceMessage) error {
	if strings.TrimSpace(text) == "" {
		return errors.New("发送内容为空")
	}
	s.mu.RLock()
	client := s.client
	s.mu.RUnlock()
	if client == nil {
		return errors.New("Telegram 客户端尚未就绪")
	}
	sender := message.NewSender(client.API())
	for _, target := range route.Targets {
		peer, err := s.peer(target.ChatID)
		if err != nil {
			return err
		}
		builder := sender.To(peer)
		var send *message.Builder
		if target.TopicID > 0 {
			send = builder.Reply(target.TopicID)
		} else {
			send = &builder.Builder
		}
		updates, err := send.Text(ctx, text)
		if err != nil {
			return fmt.Errorf("发送到 %s: %w", target.Title, err)
		}
		m.recordMapping(route.ID, source, target.ChatID, sentMessageID(updates))
		m.activity("info", "sent", "已同步到 "+displayPeer(target), route.ID)
	}
	return nil
}

func (m *Manager) forward(ctx context.Context, s *accountSession, route domain.Route, sourceChatID int64, messageID int) error {
	s.mu.RLock()
	client := s.client
	s.mu.RUnlock()
	if client == nil {
		return errors.New("Telegram 客户端尚未就绪")
	}
	from, err := s.peer(sourceChatID)
	if err != nil {
		return err
	}
	sender := message.NewSender(client.API())
	for _, target := range route.Targets {
		peer, err := s.peer(target.ChatID)
		if err != nil {
			return err
		}
		builder := sender.To(peer)
		var send *message.Builder
		if target.TopicID > 0 {
			send = builder.Reply(target.TopicID)
		} else {
			send = &builder.Builder
		}
		updates, err := send.ForwardIDs(from, messageID).Send(ctx)
		if err != nil {
			return fmt.Errorf("转发到 %s: %w", target.Title, err)
		}
		m.recordMapping(route.ID, &sourceMessage{chatID: sourceChatID, messageID: messageID}, target.ChatID, sentMessageID(updates))
		m.activity("info", "sent", "已转发到 "+displayPeer(target), route.ID)
	}
	return nil
}

func (s *accountSession) peer(chatID int64) (tg.InputPeerClass, error) {
	s.mu.RLock()
	peer, ok := s.peers[chatID]
	s.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("找不到 Chat ID %d，请确认账号已加入该会话并重新连接", chatID)
	}
	return peer, nil
}

func (a *webAuthenticator) Phone(context.Context) (string, error) { return a.phone, nil }

func (a *webAuthenticator) Code(ctx context.Context, _ *tg.AuthSentCode) (string, error) {
	a.markReady()
	if err := a.store.UpdateAccountStatus(a.accountID, "awaiting_code", "", "", 0); err != nil {
		return "", err
	}
	select {
	case code := <-a.code:
		_ = a.store.UpdateAccountStatus(a.accountID, "connecting", "", "", 0)
		return code, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (a *webAuthenticator) Password(ctx context.Context) (string, error) {
	a.markReady()
	if err := a.store.UpdateAccountStatus(a.accountID, "awaiting_password", "", "", 0); err != nil {
		return "", err
	}
	select {
	case password := <-a.password:
		_ = a.store.UpdateAccountStatus(a.accountID, "connecting", "", "", 0)
		return password, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (a *webAuthenticator) markReady() {
	a.readyOnce.Do(func() {
		close(a.ready)
	})
}

func (*webAuthenticator) AcceptTermsOfService(context.Context, tg.HelpTermsOfService) error {
	return errors.New("工作台不支持注册新 Telegram 账号，请先在官方客户端完成注册")
}

func (*webAuthenticator) SignUp(context.Context) (auth.UserInfo, error) {
	return auth.UserInfo{}, errors.New("不支持注册新 Telegram 账号")
}

type encryptedSessionStorage struct {
	path  string
	vault *vault.Vault
}

var _ session.Storage = (*encryptedSessionStorage)(nil)

func (s *encryptedSessionStorage) LoadSession(context.Context) ([]byte, error) {
	encrypted, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return nil, session.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	plain, err := s.vault.Decrypt(encrypted)
	return []byte(plain), err
}

func (s *encryptedSessionStorage) StoreSession(_ context.Context, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	encrypted, err := s.vault.Encrypt(string(data))
	if err != nil {
		return err
	}
	temp := s.path + ".tmp"
	if err := os.WriteFile(temp, encrypted, 0o600); err != nil {
		return err
	}
	return os.Rename(temp, s.path)
}

func peerID(peer tg.PeerClass) int64 {
	switch value := peer.(type) {
	case *tg.PeerUser:
		return value.UserID
	case *tg.PeerChat:
		return -value.ChatID
	case *tg.PeerChannel:
		return -channelIDOffset - value.ChannelID
	default:
		return 0
	}
}

func messageTopic(msg *tg.Message) int {
	header, ok := msg.ReplyTo.(*tg.MessageReplyHeader)
	if !ok || !header.ForumTopic {
		return 0
	}
	if top, ok := header.GetReplyToTopID(); ok {
		return top
	}
	return header.ReplyToMsgID
}

func matchesSource(sources []domain.PeerRef, chatID int64, topicID int) bool {
	for _, source := range sources {
		if source.ChatID == chatID && (source.TopicID == 0 || source.TopicID == topicID) {
			return true
		}
	}
	return false
}

func sourceTitle(sources []domain.PeerRef, chatID int64, topicID int) string {
	for _, source := range sources {
		if source.ChatID == chatID && (source.TopicID == 0 || source.TopicID == topicID) {
			return displayPeer(source)
		}
	}
	return fmt.Sprint(chatID)
}

func senderName(peer tg.PeerClass, entities tg.Entities) string {
	switch value := peer.(type) {
	case *tg.PeerUser:
		if user := entities.Users[value.UserID]; user != nil {
			name := strings.TrimSpace(user.FirstName + " " + user.LastName)
			if name != "" {
				return name
			}
			return user.Username
		}
	case *tg.PeerChat:
		if chat := entities.Chats[value.ChatID]; chat != nil {
			return chat.Title
		}
	case *tg.PeerChannel:
		if channel := entities.Channels[value.ChannelID]; channel != nil {
			return channel.Title
		}
	}
	return "未知发送者"
}

func classifyMessage(msg *tg.Message) string {
	if msg.Media == nil {
		return "text"
	}
	switch msg.Media.(type) {
	case *tg.MessageMediaWebPage, *tg.MessageMediaEmpty:
		return "text"
	case *tg.MessageMediaPhoto:
		return "photo"
	case *tg.MessageMediaDocument:
		return "document"
	case *tg.MessageMediaPoll:
		return "poll"
	case *tg.MessageMediaGeo, *tg.MessageMediaGeoLive, *tg.MessageMediaVenue:
		return "location"
	case *tg.MessageMediaContact:
		return "contact"
	case *tg.MessageMediaDice:
		return "dice"
	default:
		return "media"
	}
}

func displayPeer(peer domain.PeerRef) string {
	if peer.Title != "" {
		return peer.Title
	}
	return fmt.Sprint(peer.ChatID)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (m *Manager) activity(level, category, text, routeID string) {
	if err := m.store.AddActivity(domain.Activity{Level: level, Category: category, Message: text, RouteID: routeID}); err != nil {
		m.log.Error("record runtime activity", "error", err)
	}
}
