package domain

import "time"

type Account struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Phone       string    `json:"phone"`
	APIID       int       `json:"apiId"`
	HasAPIHash  bool      `json:"hasApiHash"`
	Status      string    `json:"status"`
	Username    string    `json:"username"`
	UserID      int64     `json:"userId"`
	LastError   string    `json:"lastError"`
	ConnectedAt time.Time `json:"connectedAt"`
	CreatedAt   time.Time `json:"createdAt"`
}

type AccountInput struct {
	Name    string `json:"name"`
	Phone   string `json:"phone"`
	APIID   int    `json:"apiId"`
	APIHash string `json:"apiHash"`
}

type PeerRef struct {
	ChatID  int64  `json:"chatId"`
	TopicID int    `json:"topicId"`
	Title   string `json:"title"`
	Kind    string `json:"kind"`
}

type Route struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	AccountID     string    `json:"accountId"`
	Sources       []PeerRef `json:"sources"`
	Targets       []PeerRef `json:"targets"`
	Mode          string    `json:"mode"`
	ReviewMode    string    `json:"reviewMode"`
	Enabled       bool      `json:"enabled"`
	SyncEdits     bool      `json:"syncEdits"`
	SyncDeletes   bool      `json:"syncDeletes"`
	SyncReactions bool      `json:"syncReactions"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
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
}

type Dashboard struct {
	RunningRoutes int        `json:"runningRoutes"`
	TotalRoutes   int        `json:"totalRoutes"`
	PendingReview int        `json:"pendingReview"`
	SentToday     int        `json:"sentToday"`
	FailedToday   int        `json:"failedToday"`
	Accounts      []Account  `json:"accounts"`
	Recent        []Activity `json:"recentActivity"`
}

type Settings struct {
	ListenAddress    string `json:"listenAddress"`
	RetentionDays    int    `json:"retentionDays"`
	MediaCacheMB     int    `json:"mediaCacheMb"`
	OpenBrowser      bool   `json:"openBrowser"`
	StartWithWindows bool   `json:"startWithWindows"`
	ProxyURL         string `json:"proxyUrl"`
}
