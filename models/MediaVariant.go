package models

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
)

type MediaVariant struct {
	ObjectKey string `json:"object_key"`
	Width     int    `json:"width"`
	Format    string `json:"format"`
	Bytes     int64  `json:"bytes,omitempty"`
}

type MediaVariants []MediaVariant

func (v MediaVariants) Value() (driver.Value, error) {
	if v == nil {
		return []byte("[]"), nil
	}
	return json.Marshal(v)
}

func (v *MediaVariants) Scan(value interface{}) error {
	if value == nil {
		*v = MediaVariants{}
		return nil
	}
	var raw []byte
	switch typed := value.(type) {
	case []byte:
		raw = typed
	case string:
		raw = []byte(typed)
	default:
		return fmt.Errorf("scan media variants: unsupported type %T", value)
	}
	if len(raw) == 0 {
		*v = MediaVariants{}
		return nil
	}
	return json.Unmarshal(raw, v)
}
