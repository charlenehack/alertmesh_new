package model

import "time"

// AIReport stores a periodic AI-generated summary report covering all incidents
// within a given time range.  It is created on demand from the /ai-reports endpoint.
type AIReport struct {
	ID        string    `gorm:"primaryKey;type:uuid;default:gen_random_uuid()" json:"id"`
	Period    string    `gorm:"not null"                                       json:"period"`     // day / week / month
	StartTime time.Time `gorm:"not null"                                       json:"start_time"` // inclusive
	EndTime   time.Time `gorm:"not null"                                       json:"end_time"`   // exclusive
	Status    string    `gorm:"not null;default:'pending'"                     json:"status"`     // pending/running/done/failed
	Report    string    `gorm:"type:text"                                      json:"report"`     // Markdown output
	Error     string    `gorm:"type:text"                                      json:"error,omitempty"`

	Timestamps
}

func (AIReport) TableName() string { return "ai_reports" }

const (
	AIReportStatusPending = "pending"
	AIReportStatusRunning = "running"
	AIReportStatusDone    = "done"
	AIReportStatusFailed  = "failed"
)
