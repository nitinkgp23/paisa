package model

import (
	"time"
	"gorm.io/gorm"
)

// KiteAuth stores Kite Connect authentication data
type KiteAuth struct {
	ID          uint      `json:"id" gorm:"primaryKey"`
	RequestToken string   `json:"request_token" gorm:"uniqueIndex"`
	AccessToken  string   `json:"access_token"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// TableName specifies the table name for KiteAuth
func (KiteAuth) TableName() string {
	return "kite_auth"
}

// GetLatestAuth retrieves the current Kite authentication data
func GetLatestAuth(db *gorm.DB) (*KiteAuth, error) {
	var auth KiteAuth
	err := db.First(&auth).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &auth, nil
}

// StoreRequestToken stores a new request token (replaces any existing entry)
func StoreRequestToken(db *gorm.DB, requestToken string) error {
	// Use Upsert to either update existing entry or create new one
	var auth KiteAuth
	result := db.First(&auth)
	
	if result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			// No existing entry, create new one
			auth = KiteAuth{
				RequestToken: requestToken,
			}
			return db.Create(&auth).Error
		}
		return result.Error
	}
	
	// Update existing entry
	auth.RequestToken = requestToken
	return db.Save(&auth).Error
}

// UpdateAccessToken updates the access token for the current entry
func UpdateAccessToken(db *gorm.DB, accessToken string) error {
	// First get the latest auth record
	auth, err := GetLatestAuth(db)
	if err != nil {
		return err
	}
	
	if auth == nil {
		return gorm.ErrRecordNotFound
	}
	
	// Update the specific record
	return db.Model(auth).
		Update("access_token", accessToken).Error
}

// ClearAuth clears all authentication data
func ClearAuth(db *gorm.DB) error {
	return db.Delete(&KiteAuth{}).Error
}