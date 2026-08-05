package domain

import "time"

const PlatformTelegram = "telegram"

type Account struct {
	ID                 string            `json:"id"`
	Platform           string            `json:"platform"`
	Name               string            `json:"name"`
	Phone              string            `json:"phone"`
	APIID              int               `json:"apiId"`
	Config             map[string]string `json:"connectorConfig"`
	HasConnectorSecret bool              `json:"hasConnectorSecret"`
	HasAPIHash         bool              `json:"hasApiHash"`
	Status             string            `json:"status"`
	Username           string            `json:"username"`
	UserID             int64             `json:"userId"`
	LastError          string            `json:"lastError"`
	ConnectedAt        time.Time         `json:"connectedAt"`
	CreatedAt          time.Time         `json:"createdAt"`
}

type AccountInput struct {
	Platform         string            `json:"platform"`
	Name             string            `json:"name"`
	Phone            string            `json:"phone"`
	APIID            int               `json:"apiId"`
	APIHash          string            `json:"apiHash"`
	ConnectorConfig  map[string]string `json:"connectorConfig"`
	ConnectorSecrets map[string]string `json:"connectorSecrets"`
}

type PeerRef struct {
	Platform    string `json:"platform"`
	ConnectorID string `json:"connectorId"`
	ChatID      int64  `json:"chatId"`
	TopicID     int    `json:"topicId"`
	Title       string `json:"title"`
	Kind        string `json:"kind"`
}

type Route struct {
	ID                 string    `json:"id"`
	Name               string    `json:"name"`
	AccountID          string    `json:"accountId"`
	SenderAccountIDs   []string  `json:"senderAccountIds"`
	Sources            []PeerRef `json:"sources"`
	Targets            []PeerRef `json:"targets"`
	Mode               string    `json:"mode"`
	ReviewMode         string    `json:"reviewMode"`
	SenderFilterMode   string    `json:"senderFilterMode"`
	AllowedSenderIDs   []int64   `json:"allowedSenderIds"`
	IncludeBots        bool      `json:"includeBots"`
	ReverseOwnMessages bool      `json:"reverseOwnMessages"`
	ButtonPolicy       string    `json:"buttonPolicy"`
	AIEnabled          bool      `json:"aiEnabled"`
	AIPrompt           string    `json:"aiPrompt"`
	Enabled            bool      `json:"enabled"`
	SyncEdits          bool      `json:"syncEdits"`
	SyncDeletes        bool      `json:"syncDeletes"`
	SyncReactions      bool      `json:"syncReactions"`
	CreatedAt          time.Time `json:"createdAt"`
	UpdatedAt          time.Time `json:"updatedAt"`
}

type Rule struct {
	ID            string    `json:"id"`
	RouteID       string    `json:"routeId"`
	Name          string    `json:"name"`
	Order         int       `json:"order"`
	Kind          string    `json:"kind"`
	Enabled       bool      `json:"enabled"`
	Pattern       string    `json:"pattern"`
	Replacement   string    `json:"replacement"`
	MessageType   string    `json:"messageType"`
	CaseSensitive bool      `json:"caseSensitive"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

type MessageEnvelope struct {
	Platform     string            `json:"platform"`
	AccountID    string            `json:"accountId"`
	RouteID      string            `json:"routeId"`
	SourceChatID int64             `json:"sourceChatId"`
	SourceMsgID  int               `json:"sourceMessageId"`
	TopicID      int               `json:"topicId"`
	SenderID     int64             `json:"senderId"`
	SenderName   string            `json:"senderName"`
	MessageType  string            `json:"messageType"`
	Text         string            `json:"text"`
	Caption      string            `json:"caption"`
	Metadata     map[string]string `json:"metadata"`
	ReplyToID    string            `json:"replyToId"`
	ThreadKey    string            `json:"threadKey"`
	Buttons      []ButtonLink      `json:"buttons"`
	Attachments  []Attachment      `json:"attachments"`
}

type Attachment struct {
	Kind     string `json:"kind"`
	MimeType string `json:"mimeType"`
	FileName string `json:"fileName"`
	Size     int64  `json:"size"`
	Ref      string `json:"ref"`
}

type TransformResult struct {
	Text     string   `json:"text"`
	Caption  string   `json:"caption"`
	Decision string   `json:"decision"`
	Matched  []string `json:"matchedRules"`
	Warnings []string `json:"warnings"`
}

type ReviewItem struct {
	ID           string    `json:"id"`
	RouteID      string    `json:"routeId"`
	RouteName    string    `json:"routeName"`
	SourceChatID int64     `json:"sourceChatId"`
	SourceTitle  string    `json:"sourceTitle"`
	SenderName   string    `json:"senderName"`
	MessageType  string    `json:"messageType"`
	OriginalText string    `json:"originalText"`
	FinalText    string    `json:"finalText"`
	Status       string    `json:"status"`
	Reason       string    `json:"reason"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

type Activity struct {
	ID        string    `json:"id"`
	Level     string    `json:"level"`
	Category  string    `json:"category"`
	Message   string    `json:"message"`
	RouteID   string    `json:"routeId"`
	CreatedAt time.Time `json:"createdAt"`
}

type MessageMapping struct {
	RouteID         string
	SourceChatID    int64
	SourceMessageID int
	TargetChatID    int64
	TargetMessageID int
	SenderAccountID string
}

type Dashboard struct {
	RunningRoutes  int        `json:"runningRoutes"`
	TotalRoutes    int        `json:"totalRoutes"`
	PendingReview  int        `json:"pendingReview"`
	SentToday      int        `json:"sentToday"`
	FailedToday    int        `json:"failedToday"`
	QueuedMessages int        `json:"queuedMessages"`
	Accounts       []Account  `json:"accounts"`
	Recent         []Activity `json:"recentActivity"`
}

type Settings struct {
	ListenAddress    string           `json:"listenAddress"`
	RetentionDays    int              `json:"retentionDays"`
	MediaCacheMB     int              `json:"mediaCacheMb"`
	OpenBrowser      bool             `json:"openBrowser"`
	StartWithWindows bool             `json:"startWithWindows"`
	ProxyURL         string           `json:"proxyUrl"`
	Delivery         DeliverySettings `json:"delivery"`
	Telegram         TelegramSettings `json:"telegram"`
	AI               AISettings       `json:"ai"`
}

type DeliverySettings struct {
	Paused             bool `json:"paused"`
	MinIntervalSeconds int  `json:"minIntervalSeconds"`
	DailyLimit         int  `json:"dailyLimit"`
}

type TelegramSettings struct {
	APIID      int    `json:"apiId"`
	APIHash    string `json:"apiHash"`
	HasAPIHash bool   `json:"hasApiHash"`
}

type AISettings struct {
	Enabled        bool   `json:"enabled"`
	BaseURL        string `json:"baseUrl"`
	Model          string `json:"model"`
	APIKey         string `json:"apiKey"`
	HasAPIKey      bool   `json:"hasApiKey"`
	Prompt         string `json:"prompt"`
	TimeoutSeconds int    `json:"timeoutSeconds"`
	FailurePolicy  string `json:"failurePolicy"`
	MaxInputChars  int    `json:"maxInputChars"`
}

type ButtonLink struct {
	Text string `json:"text"`
	URL  string `json:"url"`
}

type OutboxJob struct {
	ID                string       `json:"id"`
	RouteID           string       `json:"routeId"`
	RouteName         string       `json:"routeName"`
	Platform          string       `json:"platform"`
	Target            PeerRef      `json:"target"`
	Text              string       `json:"text"`
	Buttons           []ButtonLink `json:"buttons"`
	SourceChatID      int64        `json:"sourceChatId"`
	SourceMessageID   int          `json:"sourceMessageId"`
	SenderAccountIDs  []string     `json:"senderAccountIds"`
	OrderKey          string       `json:"orderKey"`
	DedupeKey         string       `json:"dedupeKey"`
	RandomID          int64        `json:"randomId"`
	Status            string       `json:"status"`
	Attempts          int          `json:"attempts"`
	AssignedAccountID string       `json:"assignedAccountId"`
	LastError         string       `json:"lastError"`
	AvailableAt       time.Time    `json:"availableAt"`
	LeaseUntil        time.Time    `json:"leaseUntil"`
	CreatedAt         time.Time    `json:"createdAt"`
	UpdatedAt         time.Time    `json:"updatedAt"`
}
