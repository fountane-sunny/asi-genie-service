package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

type Message struct {
	Role       string          `json:"role"`
	Content    string          `json:"content"`
	ToolCalls  json.RawMessage `json:"toolCalls,omitempty"`
	CreatedAt  time.Time       `json:"createdAt"`
	DurationMs int64           `json:"durationMs,omitempty"`
}

type Turn struct {
	ID           string          `json:"id"`
	CreatedAt    time.Time       `json:"createdAt"`
	SessionID    string          `json:"sessionId"`
	PropertyID   int             `json:"propertyId"`
	UserID       string          `json:"userId,omitempty"`
	UserName     string          `json:"userName,omitempty"`
	UserEmail    string          `json:"userEmail,omitempty"`
	Message      string          `json:"message"` // PAN-masked before it is stored
	Reply        string          `json:"reply"`
	Cards        json.RawMessage `json:"cards,omitempty"`
	Intent       string          `json:"intent,omitempty"`
	ScenarioID   string          `json:"scenarioId,omitempty"`
	Source       string          `json:"source,omitempty"`
	Confidence   float64         `json:"confidence,omitempty"`
	Slots        json.RawMessage `json:"slots,omitempty"`
	Tools        json.RawMessage `json:"tools,omitempty"`
	LLMModel     string          `json:"llmModel,omitempty"`
	LLMFailed    bool            `json:"llmFailed,omitempty"`
	DurationMs   int64           `json:"durationMs"`
	ErrorCode    string          `json:"errorCode,omitempty"`
	ErrorMessage string          `json:"errorMessage,omitempty"`
	Feedback     *string         `json:"feedback,omitempty"`
}

// Property id 0 is not every property. From and To zero means unbounded.
type TurnFilter struct {
	From, To   time.Time
	PropertyID int
	UserID     string
	SessionID  string
	Limit      int
}

// Property id 0 is not every property.
func ValidateScope(propertyID int, userID string) error {
	if propertyID <= 0 {
		return fmt.Errorf("propertyId is required")
	}
	if strings.TrimSpace(userID) == "" {
		return fmt.Errorf("userId is required")
	}
	return nil
}

type ToolExecution struct {
	SessionID string
	ToolName  string
	Method    string
	Path      string
	Args      json.RawMessage
	Status    string
	Result    json.RawMessage
}

// Redacted. Kind is intent, denied, unsupported, or pci_block.
type Event struct {
	SessionID  string          `json:"sessionId"`
	Kind       string          `json:"kind"`
	Intent     string          `json:"intent,omitempty"`
	ScenarioID string          `json:"scenarioId,omitempty"`
	PropertyID int             `json:"propertyId,omitempty"`
	Message    string          `json:"message,omitempty"` // redacted; unsupported asks only
	Detail     json.RawMessage `json:"detail,omitempty"`
	CreatedAt  time.Time       `json:"createdAt"`
}

type Store interface {
	// A different property starts a new conversation.
	AppendMessages(ctx context.Context, sessionID, userID string, propertyID int, msgs ...Message) error
	ReplaceTurnMessages(ctx context.Context, sessionID, userID string, propertyID int, turnID string, userMsg, assistant Message) (replaced bool, err error)
	UpdateTurn(ctx context.Context, turnID string, t Turn) error
	// A null or different user_id is not returned.
	History(ctx context.Context, sessionID, userID string, propertyID, limit int) ([]Message, error)
	LogToolExecution(ctx context.Context, e ToolExecution) error
	LogEvent(ctx context.Context, e Event) error
	// Events with an empty session id are omitted.
	Events(ctx context.Context, kind, userID string, propertyID, limit int) ([]Event, error)
	LogTurn(ctx context.Context, t Turn) (turnID string, err error)
	// Only when the turn belongs to this user and property.
	UpdateTurnFeedback(ctx context.Context, turnID, sessionID, userID string, propertyID int, feedback string) error
	Turns(ctx context.Context, f TurnFilter) ([]Turn, error)
	Close() error
}

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
