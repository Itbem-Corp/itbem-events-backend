package models

import (
	"testing"

	"gorm.io/gorm"
)

func TestAutomationCodeReviewPublicationIsAppendOnly(t *testing.T) {
	publication := &AutomationCodeReviewPublication{}
	if publication.BeforeUpdate(&gorm.DB{}) == nil || publication.BeforeDelete(&gorm.DB{}) == nil {
		t.Fatal("remote code review proof must reject updates and deletes")
	}
}
