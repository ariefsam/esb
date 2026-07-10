package projection

import (
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// ProjectionCursorRow tracks the last processed event ID per worker.
type ProjectionCursorRow struct {
	Name      string `gorm:"primaryKey"`
	AfterID   int64  `gorm:"not null;default:0"`
	UpdatedAt int64  `gorm:"not null;default:0"`
}

func (ProjectionCursorRow) TableName() string { return "projection_cursors" }

func NewProjectionDB(dsn string) (*gorm.DB, error) {
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, err
	}

	if err := db.AutoMigrate(
		&ProjectionCursorRow{},
		// esb:inject:automigrate-models
	); err != nil {
		return nil, err
	}

	return db, nil
}
