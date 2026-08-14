package model

// SavedQuery stores a user-saved observability query (PromQL or OpenSearch DSL)
// along with the natural-language prompt that produced it.
type SavedQuery struct {
	ID              string    `gorm:"primaryKey;type:uuid;default:gen_random_uuid()" json:"id"`
	Name            string    `gorm:"not null"                                       json:"name"`
	DataSourceKind  string    `gorm:"not null;type:varchar(32)"                      json:"data_source_kind"` // prometheus / opensearch / elastic
	DataSourceID    *string   `gorm:"type:uuid;index"                                json:"data_source_id,omitempty"`
	NaturalLanguage string    `gorm:"type:text"                                      json:"natural_language"`
	QueryText       string    `gorm:"type:text"                                      json:"query_text"`
	IsShared        bool      `gorm:"default:false"                                  json:"is_shared"`
	CreatedBy       *string   `gorm:"type:uuid;index"                                json:"created_by,omitempty"`

	Timestamps
}

func (SavedQuery) TableName() string { return "saved_queries" }
