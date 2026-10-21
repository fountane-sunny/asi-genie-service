package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	gormLogger "gorm.io/gorm/logger"
)

type Conversation struct {
	ID         string    `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	SessionID  string    `gorm:"type:text;uniqueIndex;not null"`
	UserID     *string   `gorm:"type:text;index"`
	PropertyID *int      `gorm:"index"`
	Title      string    `gorm:"type:text"`
	CreatedAt  time.Time `gorm:"not null;default:now()"`
	UpdatedAt  time.Time `gorm:"not null;default:now()"`
}

type MessageRow struct {
	ID             string    `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	ConversationID string    `gorm:"type:uuid;index;not null"`
	Role           string    `gorm:"type:text;not null"`
	Content        string    `gorm:"type:text"`
	ToolCalls      []byte    `gorm:"type:jsonb"`
	DurationMs     *int64    `gorm:""`
	CreatedAt      time.Time `gorm:"not null;default:now();index"`
}

type GenieTurn struct {
	ID           string    `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	CreatedAt    time.Time `gorm:"not null;default:now();index:idx_genie_turns_prop_created,priority:2;index"`
	SessionID    string    `gorm:"type:text;index"`
	PropertyID   int       `gorm:"index:idx_genie_turns_prop_created,priority:1"`
	UserID       *string   `gorm:"type:text;index"`
	UserName     *string   `gorm:"type:text"`
	UserEmail    *string   `gorm:"type:text"`
	Message      string    `gorm:"type:text"` // PAN-masked before it is stored
	Reply        string    `gorm:"type:text"`
	Cards        []byte    `gorm:"type:jsonb"`
	Intent       *string   `gorm:"type:text;index"`
	ScenarioID   *string   `gorm:"type:text"`
	Source       *string   `gorm:"type:text"`
	Confidence   float64   `gorm:""`
	Slots        []byte    `gorm:"type:jsonb"`
	Tools        []byte    `gorm:"type:jsonb"`
	LLMModel     *string   `gorm:"type:text"`
	LLMFailed    bool      `gorm:"not null;default:false"`
	DurationMs   int64     `gorm:""`
	ErrorCode    *string   `gorm:"type:text;index"`
	ErrorMessage *string   `gorm:"type:text"`
	Feedback     *string   `gorm:"type:text"`
	RetryCount   int       `gorm:"not null;default:0"`
}

func (MessageRow) TableName() string { return "messages" }

type ToolExecutionRow struct {
	ID             string    `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	ConversationID *string   `gorm:"type:uuid;index"`
	ToolName       string    `gorm:"type:text;not null"`
	Method         string    `gorm:"type:text"`
	Path           string    `gorm:"type:text"`
	Args           []byte    `gorm:"type:jsonb"`
	Status         string    `gorm:"type:text;not null"`
	Result         []byte    `gorm:"type:jsonb"`
	CreatedAt      time.Time `gorm:"not null;default:now()"`
}

func (ToolExecutionRow) TableName() string { return "tool_executions" }

type GenieEvent struct {
	ID         string    `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	SessionID  *string   `gorm:"type:text"`
	Kind       string    `gorm:"type:text;not null;index:idx_genie_events_kind_created,priority:1"`
	Intent     *string   `gorm:"type:text"`
	ScenarioID *string   `gorm:"type:text"`
	PropertyID *int      `gorm:""`
	Message    *string   `gorm:"type:text"`
	Detail     []byte    `gorm:"type:jsonb"`
	CreatedAt  time.Time `gorm:"not null;default:now();index:idx_genie_events_kind_created,priority:2"`
}

var Models = []any{&Conversation{}, &MessageRow{}, &ToolExecutionRow{}, &GenieEvent{}, &GenieTurn{}}

type pg struct{ db *gorm.DB }

func NewPostgres(dsn string) (Store, error) {
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: gormLogger.Default.LogMode(gormLogger.Silent)})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to postgres database: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("failed to get underlying sql.DB: %w", err)
	}
	sqlDB.SetMaxOpenConns(10)
	sqlDB.SetMaxIdleConns(5)
	sqlDB.SetConnMaxLifetime(30 * time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(ctx); err != nil {
		sqlDB.Close()
		return nil, err
	}
	if err := db.AutoMigrate(Models...); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("auto-migrate: %w", err)
	}
	return &pg{db: db}, nil
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func intPtr(n int) *int {
	if n == 0 {
		return nil
	}
	return &n
}

// A session belongs to one user and property. A mismatch archives the old row
// under a "~p<old>-<unix>" session id and starts a new one.
func (p *pg) conversation(ctx context.Context, sessionID, userID string, propertyID int) (string, error) {
	db := p.db.WithContext(ctx)
	var cur Conversation
	err := db.Where("session_id = ?", sessionID).Limit(1).Find(&cur).Error
	if err != nil {
		return "", err
	}
	if cur.ID != "" {
		// propertyID 0 is a tool-log lookup and must not rebind the session.
		if propertyID <= 0 {
			err = db.Model(&cur).Update("updated_at", gorm.Expr("NOW()")).Error
			return cur.ID, err
		}
		// A null user_id is not adopted; that would claim an orphaned chat.
		if cur.UserID != nil && *cur.UserID == userID && cur.PropertyID != nil && *cur.PropertyID == propertyID {
			err = db.Model(&cur).Update("updated_at", gorm.Expr("NOW()")).Error
			return cur.ID, err
		}
		oldProp := 0
		if cur.PropertyID != nil {
			oldProp = *cur.PropertyID
		}
		archived := fmt.Sprintf("%s~p%d-%d", sessionID, oldProp, time.Now().Unix())
		if err := db.Model(&cur).Update("session_id", archived).Error; err != nil {
			return "", err
		}
	}
	c := Conversation{SessionID: sessionID, UserID: strPtr(userID), PropertyID: intPtr(propertyID)}
	err = db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "session_id"}},
		DoUpdates: clause.Assignments(map[string]any{"updated_at": gorm.Expr("NOW()")}),
	}).Create(&c).Error
	if err != nil {
		return "", err
	}
	if c.ID == "" {
		err = db.Model(&Conversation{}).Where("session_id = ?", sessionID).Pluck("id", &c.ID).Error
	}
	return c.ID, err
}

func (p *pg) AppendMessages(ctx context.Context, sessionID, userID string, propertyID int, msgs ...Message) error {
	convID, err := p.conversation(ctx, sessionID, userID, propertyID)
	if err != nil {
		return err
	}
	rows := make([]MessageRow, 0, len(msgs))
	now := time.Now().UTC()
	for i, m := range msgs {
		ts := m.CreatedAt
		if ts.IsZero() {
			ts = now.Add(time.Duration(i) * time.Microsecond) // preserve order inside one turn
		}
		row := MessageRow{ConversationID: convID, Role: m.Role, Content: m.Content, ToolCalls: nilIfEmpty(m.ToolCalls), CreatedAt: ts}
		if m.DurationMs > 0 {
			d := m.DurationMs
			row.DurationMs = &d
		}
		rows = append(rows, row)
	}
	return p.db.WithContext(ctx).Create(&rows).Error
}

func (p *pg) ReplaceTurnMessages(ctx context.Context, sessionID, userID string, propertyID int, turnID string, userMsg, assistant Message) (bool, error) {
	if strings.TrimSpace(turnID) == "" {
		return false, nil
	}
	if err := ValidateScope(propertyID, userID); err != nil {
		return false, err
	}
	var conv Conversation
	err := p.db.WithContext(ctx).
		Where("session_id = ? AND user_id = ? AND property_id = ?", sessionID, userID, propertyID).
		Take(&conv).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var assistantRow MessageRow
	err = p.db.WithContext(ctx).
		Where("conversation_id = ? AND role = ? AND tool_calls->>'turnId' = ?", conv.ID, "assistant", turnID).
		Order("created_at DESC").
		Take(&assistantRow).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var userRow MessageRow
	err = p.db.WithContext(ctx).
		Where("conversation_id = ? AND role = ? AND created_at <= ?", conv.ID, "user", assistantRow.CreatedAt).
		Order("created_at DESC").
		Take(&userRow).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	err = p.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&userRow).Update("content", userMsg.Content).Error; err != nil {
			return err
		}
		assistantUpdates := map[string]any{
			"content":    assistant.Content,
			"tool_calls": nilIfEmpty(assistant.ToolCalls),
		}
		if assistant.DurationMs > 0 {
			assistantUpdates["duration_ms"] = assistant.DurationMs
		}
		return tx.Model(&assistantRow).Updates(assistantUpdates).Error
	})
	if err != nil {
		return false, err
	}
	return true, nil
}

func nilIfEmpty(b []byte) []byte {
	if len(b) == 0 {
		return nil
	}
	return b
}

func (p *pg) History(ctx context.Context, sessionID, userID string, propertyID, limit int) ([]Message, error) {
	if err := ValidateScope(propertyID, userID); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 1000
	}
	var conv Conversation
	err := p.db.WithContext(ctx).
		Where("session_id = ? AND user_id = ? AND property_id = ?", sessionID, userID, propertyID).
		Take(&conv).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	if err != nil {
		return nil, err
	}
	var rows []MessageRow
	if err := p.db.WithContext(ctx).Where("conversation_id = ?", conv.ID).Order("created_at DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]Message, 0, len(rows))
	for i := len(rows) - 1; i >= 0; i-- {
		r := rows[i]
		m := Message{Role: r.Role, Content: r.Content, ToolCalls: r.ToolCalls, CreatedAt: r.CreatedAt}
		if r.DurationMs != nil {
			m.DurationMs = *r.DurationMs
		}
		out = append(out, m)
	}
	return out, nil
}

func (p *pg) LogToolExecution(ctx context.Context, e ToolExecution) error {
	row := ToolExecutionRow{ToolName: e.ToolName, Method: e.Method, Path: e.Path, Args: nilIfEmpty(e.Args), Status: e.Status, Result: nilIfEmpty(e.Result)}
	if e.SessionID != "" {
		id, err := p.conversation(ctx, e.SessionID, "", 0)
		if err != nil {
			return err
		}
		row.ConversationID = &id
	}
	return p.db.WithContext(ctx).Create(&row).Error
}

func (p *pg) LogEvent(ctx context.Context, e Event) error {
	row := GenieEvent{SessionID: strPtr(e.SessionID), Kind: e.Kind, Intent: strPtr(e.Intent), ScenarioID: strPtr(e.ScenarioID),
		PropertyID: intPtr(e.PropertyID), Message: strPtr(e.Message), Detail: nilIfEmpty(e.Detail)}
	return p.db.WithContext(ctx).Create(&row).Error
}

func (p *pg) LogTurn(ctx context.Context, t Turn) (string, error) {
	row := GenieTurn{SessionID: t.SessionID, PropertyID: t.PropertyID, UserID: strPtr(t.UserID), UserName: strPtr(t.UserName),
		UserEmail: strPtr(t.UserEmail), Message: t.Message, Reply: t.Reply, Cards: nilIfEmpty(t.Cards), Intent: strPtr(t.Intent),
		ScenarioID: strPtr(t.ScenarioID), Source: strPtr(t.Source), Confidence: t.Confidence, Slots: nilIfEmpty(t.Slots),
		Tools: nilIfEmpty(t.Tools), LLMModel: strPtr(t.LLMModel), LLMFailed: t.LLMFailed, DurationMs: t.DurationMs,
		ErrorCode: strPtr(t.ErrorCode), ErrorMessage: strPtr(t.ErrorMessage), Feedback: t.Feedback}
	if !t.CreatedAt.IsZero() {
		row.CreatedAt = t.CreatedAt
	}
	if err := p.db.WithContext(ctx).Create(&row).Error; err != nil {
		return "", err
	}
	return row.ID, nil
}

func (p *pg) UpdateTurn(ctx context.Context, turnID string, t Turn) error {
	if strings.TrimSpace(turnID) == "" {
		return fmt.Errorf("turnId is required")
	}
	updates := map[string]any{
		"message":       t.Message,
		"reply":         t.Reply,
		"cards":         nilIfEmpty(t.Cards),
		"intent":        strPtr(t.Intent),
		"scenario_id":   strPtr(t.ScenarioID),
		"source":        strPtr(t.Source),
		"confidence":    t.Confidence,
		"slots":         nilIfEmpty(t.Slots),
		"tools":         nilIfEmpty(t.Tools),
		"llm_model":     strPtr(t.LLMModel),
		"llm_failed":    t.LLMFailed,
		"duration_ms":   t.DurationMs,
		"error_code":    strPtr(t.ErrorCode),
		"error_message": strPtr(t.ErrorMessage),
		"retry_count":   gorm.Expr("retry_count + 1"),
	}
	res := p.db.WithContext(ctx).Model(&GenieTurn{}).
		Where("id = ? AND session_id = ? AND property_id = ?", turnID, t.SessionID, t.PropertyID).
		Updates(updates)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (p *pg) UpdateTurnFeedback(ctx context.Context, turnID, sessionID, userID string, propertyID int, feedback string) error {
	if turnID == "" {
		return fmt.Errorf("turnId is required")
	}
	if err := ValidateScope(propertyID, userID); err != nil {
		return err
	}
	q := p.db.WithContext(ctx).Model(&GenieTurn{}).
		Where("id = ? AND user_id = ? AND property_id = ?", turnID, userID, propertyID)
	if sessionID != "" {
		q = q.Where("session_id = ?", sessionID)
	}
	res := q.Update("feedback", feedback)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (p *pg) Turns(ctx context.Context, f TurnFilter) ([]Turn, error) {
	if err := ValidateScope(f.PropertyID, f.UserID); err != nil {
		return nil, err
	}
	q := p.db.WithContext(ctx).Model(&GenieTurn{}).
		Where("property_id = ? AND user_id = ?", f.PropertyID, f.UserID)
	if !f.From.IsZero() {
		q = q.Where("created_at >= ?", f.From)
	}
	if !f.To.IsZero() {
		q = q.Where("created_at < ?", f.To)
	}
	if f.SessionID != "" {
		q = q.Where("session_id = ?", f.SessionID)
	}
	if f.Limit <= 0 || f.Limit > 10000 {
		f.Limit = 10000
	}
	var rows []GenieTurn
	if err := q.Order("created_at").Limit(f.Limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	s := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	out := make([]Turn, 0, len(rows))
	for _, r := range rows {
		out = append(out, Turn{ID: r.ID, CreatedAt: r.CreatedAt, SessionID: r.SessionID, PropertyID: r.PropertyID,
			UserID: s(r.UserID), UserName: s(r.UserName), UserEmail: s(r.UserEmail), Message: r.Message, Reply: r.Reply,
			Cards: r.Cards, Intent: s(r.Intent), ScenarioID: s(r.ScenarioID), Source: s(r.Source), Confidence: r.Confidence,
			Slots: r.Slots, Tools: r.Tools, LLMModel: s(r.LLMModel), LLMFailed: r.LLMFailed, DurationMs: r.DurationMs,
			ErrorCode: s(r.ErrorCode), ErrorMessage: s(r.ErrorMessage), Feedback: r.Feedback})
	}
	return out, nil
}

func (p *pg) Close() error {
	sqlDB, err := p.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}
