// Package store persists Genie conversations, messages and tool executions.
// Postgres (store_postgres.go) is the default; the in-memory store is a
// fallback when no database is configured.
package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// Message is one persisted chat turn.
type Message struct {
	Role      string          `json:"role"`
	Content   string          `json:"content"`
	ToolCalls json.RawMessage `json:"toolCalls,omitempty"`
	CreatedAt time.Time       `json:"createdAt"`
	// DurationMs is the server time to produce an assistant reply (0 for user messages).
	DurationMs int64 `json:"durationMs,omitempty"`
}

// Turn is one analytics row per /chat turn (see genie_turns).
type Turn struct {
	ID           string          `json:"id"`
	CreatedAt    time.Time       `json:"createdAt"`
	SessionID    string          `json:"sessionId"`
	PropertyID   int             `json:"propertyId"`
	UserID       string          `json:"userId,omitempty"`
	UserName     string          `json:"userName,omitempty"`
	UserEmail    string          `json:"userEmail,omitempty"`
	Message      string          `json:"message"` // PAN-masked
	Reply        string          `json:"reply"`
	Cards        json.RawMessage `json:"cards,omitempty"`
	Intent       string          `json:"intent,omitempty"`
	ScenarioID   string          `json:"scenarioId,omitempty"`
	Source       string          `json:"source,omitempty"` // llm | keyword | rule
	Confidence   float64         `json:"confidence,omitempty"`
	Slots        json.RawMessage `json:"slots,omitempty"`
	Tools        json.RawMessage `json:"tools,omitempty"` // [{name,status,error,durationMs}]
	LLMModel     string          `json:"llmModel,omitempty"`
	LLMFailed    bool            `json:"llmFailed,omitempty"`
	DurationMs   int64           `json:"durationMs"`
	ErrorCode    string          `json:"errorCode,omitempty"`
	ErrorMessage string          `json:"errorMessage,omitempty"`
	Feedback     *string         `json:"feedback,omitempty"` // for later (thumbs up/down, comment)
}

// TurnFilter selects turns for export.
// PropertyID and UserID are both required. A zero property id is not "all properties".
type TurnFilter struct {
	From, To   time.Time // zero = open
	PropertyID int       // required, > 0
	UserID     string    // required
	SessionID  string    // optional
	Limit      int
}

// ValidateScope rejects a missing user or a missing/zero property.
// Callers must not treat property id 0 as every property.
func ValidateScope(propertyID int, userID string) error {
	if propertyID <= 0 {
		return fmt.Errorf("propertyId is required")
	}
	if strings.TrimSpace(userID) == "" {
		return fmt.Errorf("userId is required")
	}
	return nil
}

// ToolExecution is an audit row for a tool call.
type ToolExecution struct {
	SessionID string
	ToolName  string
	Method    string
	Path      string
	Args      json.RawMessage
	Status    string
	Result    json.RawMessage
}

// Event is a redacted analytics event (Phase 1 audit = analytics only):
// kind is "intent", "denied", "unsupported" or "pci_block".
type Event struct {
	SessionID  string          `json:"sessionId"`
	Kind       string          `json:"kind"`
	Intent     string          `json:"intent,omitempty"`
	ScenarioID string          `json:"scenarioId,omitempty"`
	PropertyID int             `json:"propertyId,omitempty"`
	Message    string          `json:"message,omitempty"` // redacted user text (unsupported asks only)
	Detail     json.RawMessage `json:"detail,omitempty"`
	CreatedAt  time.Time       `json:"createdAt"`
}

// Store is implemented by the memory and Postgres backends.
type Store interface {
	// AppendMessages appends msgs to sessionID's conversation, which is bound to
	// propertyID: a new conversation is started if the property differs.
	AppendMessages(ctx context.Context, sessionID, userID string, propertyID int, msgs ...Message) error
	// History returns the last limit messages (oldest first) for this user and property.
	// A conversation with a null or different user_id is not returned.
	// limit<=0 means all. Missing or unowned conversation returns an error.
	History(ctx context.Context, sessionID, userID string, propertyID, limit int) ([]Message, error)
	LogToolExecution(ctx context.Context, e ToolExecution) error
	LogEvent(ctx context.Context, e Event) error
	// Events returns recent events of a kind ("" = all), newest last, for this
	// user's sessions at this property. Events with an empty session id are omitted.
	Events(ctx context.Context, kind, userID string, propertyID, limit int) ([]Event, error)
	// LogTurn stores one analytics row per chat turn and returns the new turn id.
	LogTurn(ctx context.Context, t Turn) (turnID string, err error)
	// UpdateTurnFeedback sets feedback when the turn belongs to this user and property.
	UpdateTurnFeedback(ctx context.Context, turnID, sessionID, userID string, propertyID int, feedback string) error
	// Turns returns turns matching f, oldest first.
	Turns(ctx context.Context, f TurnFilter) ([]Turn, error)
	Close() error
}

// PostgresDSN builds a DSN the way asi-reservation-service does
// (host/port/user/password/dbname/sslmode from POSTGRES_*), falling back to
// DATABASE_URL. Returns "" when neither is configured.
func PostgresDSN() string {
	host := os.Getenv("POSTGRES_HOST")
	if host != "" && os.Getenv("POSTGRES_USER") != "" {
		port := os.Getenv("POSTGRES_PORT")
		if port == "" {
			port = "5432"
		}
		ssl := os.Getenv("POSTGRES_SSLMODE")
		if ssl == "" {
			ssl = "disable"
		}
		return fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
			host, port, os.Getenv("POSTGRES_USER"), os.Getenv("POSTGRES_PASSWORD"), os.Getenv("POSTGRES_DB"), ssl)
	}
	return os.Getenv("DATABASE_URL")
}
