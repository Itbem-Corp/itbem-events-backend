package dtos

// MediaProcessMessage is the payload sent to the async media processor.
type MediaProcessMessage struct {
	MomentID    string `json:"moment_id"`
	EventID     string `json:"event_id"`
	JobID       string `json:"job_id,omitempty"`
	Generation  int64  `json:"generation,omitempty"`
	ObjectKey   string `json:"object_key,omitempty"`
	RawS3Key    string `json:"raw_s3_key"`
	Bucket      string `json:"bucket"`
	ContentType string `json:"content_type"`
	IsVideo     bool   `json:"is_video"`
}

// MediaProcessingCallback is the normalized Lambda callback contract. JobID
// and Generation are optional only for already-enqueued legacy messages.
type MediaProcessingCallback struct {
	MomentID             string
	EventID              string
	JobID                string
	Generation           int64
	ObjectKey            string
	ThumbnailObjectKey   string
	ProcessingStatus     string
	ErrorMessage         string
	ProcessingDurationMs int64
	OriginalSizeBytes    int64
	OptimizedSizeBytes   int64
}

func NewMediaProcessMessage(momentID, eventID, objectKey, bucket, contentType string, isVideo bool) MediaProcessMessage {
	return MediaProcessMessage{
		MomentID:    momentID,
		EventID:     eventID,
		ObjectKey:   objectKey,
		RawS3Key:    objectKey,
		Bucket:      bucket,
		ContentType: contentType,
		IsVideo:     isVideo,
	}
}

func (m MediaProcessMessage) StorageObjectKey() string {
	if m.ObjectKey != "" {
		return m.ObjectKey
	}
	return m.RawS3Key
}
