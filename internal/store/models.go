package store

// Account is a bound WeRead account.
type Account struct {
	ID             int64  `json:"id"`
	Name           string `json:"name"`
	Vid            string `json:"vid"`
	Nickname       string `json:"nickname"`
	Avatar         string `json:"avatar"`
	Status         string `json:"status"`
	LastError      string `json:"last_error"`
	LastCheckAt    int64  `json:"last_check_at"`
	DefaultMinutes int    `json:"default_minutes"`
	TickSeconds    int    `json:"tick_seconds"`
	BookID         string `json:"book_id"`
	BookTitle      string `json:"book_title"`
	ChapterID      string `json:"chapter_id"`
	ChapterTitle   string `json:"chapter_title"`
	Mode           string `json:"mode"`
	PushChannelID  int64  `json:"push_channel_id"`
	HasPayload     bool   `json:"has_payload"`
	Running        bool   `json:"running"`
	CreatedAt      int64  `json:"created_at"`
	UpdatedAt      int64  `json:"updated_at"`
}

// Run is one reading session.
type Run struct {
	ID             int64  `json:"id"`
	AccountID      int64  `json:"account_id"`
	AccountName    string `json:"account_name,omitempty"`
	Trigger        string `json:"trigger"`
	PlanSeconds    int    `json:"plan_seconds"`
	ElapsedSeconds int    `json:"elapsed_seconds"`
	SuccessTicks   int    `json:"success_ticks"`
	FailedTicks    int    `json:"failed_ticks"`
	Status         string `json:"status"`
	Error          string `json:"error"`
	StartedAt      int64  `json:"started_at"`
	FinishedAt     int64  `json:"finished_at"`
}

// RunLog is one line of a run's log stream.
type RunLog struct {
	ID      int64  `json:"id"`
	RunID   int64  `json:"run_id"`
	TS      int64  `json:"ts"`
	Level   string `json:"level"`
	Message string `json:"message"`
}

// Schedule is a cron-triggered reading plan.
type Schedule struct {
	ID          int64  `json:"id"`
	AccountID   int64  `json:"account_id"`
	AccountName string `json:"account_name,omitempty"`
	Cron        string `json:"cron"`
	Minutes     int    `json:"minutes"`
	Enabled     bool   `json:"enabled"`
	LastRunAt   int64  `json:"last_run_at"`
	NextRunAt   int64  `json:"next_run_at"`
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`
}

// PushChannel is a notification integration.
type PushChannel struct {
	ID        int64             `json:"id"`
	Name      string            `json:"name"`
	Type      string            `json:"type"`
	Config    map[string]string `json:"config"`
	Enabled   bool              `json:"enabled"`
	CreatedAt int64             `json:"created_at"`
}

// Session is a panel login session.
type Session struct {
	ID        string `json:"id"`
	CreatedAt int64  `json:"created_at"`
	ExpiresAt int64  `json:"expires_at"`
	IP        string `json:"ip"`
	UA        string `json:"ua"`
}
