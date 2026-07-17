package dtos

// MediaProcessMessage is the payload sent to the async media processor.
type MediaProcessMessage struct {
	Application    string `json:"application,omitempty"`
	CorrelationID  string `json:"correlation_id,omitempty"`
	SourceRevision string `json:"source_revision,omitempty"`
	TargetType     string `json:"target_type,omitempty"`
	MomentID       string `json:"moment_id"`
	EventID        string `json:"event_id"`
	JobID          string `json:"job_id,omitempty"`
	Generation     int64  `json:"generation,omitempty"`
	ObjectKey      string `json:"object_key,omitempty"`
	RawS3Key       string `json:"raw_s3_key"`
	Bucket         string `json:"bucket"`
	ContentType    string `json:"content_type"`
	IsVideo        bool   `json:"is_video"`
}

const (
	MediaTargetMoment     = "moment"
	MediaTargetEventCover = "event_cover"
)

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
	MediaVariants        []MediaVariant
}

type MediaVariant struct {
	ObjectKey string `json:"object_key"`
	Width     int    `json:"width"`
	Format    string `json:"format"`
	Bytes     int64  `json:"bytes,omitempty"`
}

func NewMediaProcessMessage(momentID, eventID, objectKey, bucket, contentType string, isVideo bool) MediaProcessMessage {
	return MediaProcessMessage{
		TargetType:  MediaTargetMoment,
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
