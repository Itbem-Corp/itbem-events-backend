package dtos

import (
	"encoding/json"
	"sort"
	"strings"
)

type CompletedUploadPart struct {
	PartNumber int    `json:"part_number"`
	ETag       string `json:"etag"`
}

func (p *CompletedUploadPart) UnmarshalJSON(data []byte) error {
	type alias CompletedUploadPart
	var raw struct {
		alias
		PartNumberCamel  *int   `json:"partNumber"`
		PartNumberPascal *int   `json:"PartNumber"`
		ETagPascal       string `json:"ETag"`
		ETagCamel        string `json:"eTag"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	*p = CompletedUploadPart(raw.alias)
	if p.PartNumber == 0 {
		switch {
		case raw.PartNumberCamel != nil:
			p.PartNumber = *raw.PartNumberCamel
		case raw.PartNumberPascal != nil:
			p.PartNumber = *raw.PartNumberPascal
		}
	}
	if p.ETag == "" {
		switch {
		case raw.ETagPascal != "":
			p.ETag = raw.ETagPascal
		case raw.ETagCamel != "":
			p.ETag = raw.ETagCamel
		}
	}
	return nil
}

func NormalizeCompletedUploadParts(parts []CompletedUploadPart) []CompletedUploadPart {
	byPartNumber := make(map[int]CompletedUploadPart, len(parts))
	for _, part := range parts {
		if part.PartNumber <= 0 {
			continue
		}
		part.ETag = strings.TrimSpace(part.ETag)
		if part.ETag == "" {
			continue
		}
		if _, exists := byPartNumber[part.PartNumber]; exists {
			continue
		}
		byPartNumber[part.PartNumber] = part
	}

	normalized := make([]CompletedUploadPart, 0, len(byPartNumber))
	for _, part := range byPartNumber {
		normalized = append(normalized, part)
	}
	sort.SliceStable(normalized, func(i, j int) bool {
		return normalized[i].PartNumber < normalized[j].PartNumber
	})
	return normalized
}

type PresignedUploadPart struct {
	PartNumber int    `json:"part_number"`
	URL        string `json:"url"`
}

type UploadQuotaMetadata struct {
	UploadsLimit     *int64 `json:"uploads_limit,omitempty"`
	UploadsUsed      *int64 `json:"uploads_used,omitempty"`
	UploadsRemaining *int64 `json:"uploads_remaining,omitempty"`
}

func NewUploadQuotaMetadata(limit, used, remaining int64) UploadQuotaMetadata {
	return UploadQuotaMetadata{
		UploadsLimit:     &limit,
		UploadsUsed:      &used,
		UploadsRemaining: &remaining,
	}
}

type MomentUploadURLResponse struct {
	UploadURL   string `json:"upload_url"`
	ObjectKey   string `json:"object_key"`
	S3Key       string `json:"s3_key"`
	ContentType string `json:"content_type,omitempty"`
	UploadQuotaMetadata
}

func NewMomentUploadURLResponse(uploadURL, objectKey, contentType string) MomentUploadURLResponse {
	return MomentUploadURLResponse{
		UploadURL:   uploadURL,
		ObjectKey:   objectKey,
		S3Key:       objectKey,
		ContentType: contentType,
	}
}

func NewMomentUploadURLResponseWithQuota(uploadURL, objectKey, contentType string, quota UploadQuotaMetadata) MomentUploadURLResponse {
	response := NewMomentUploadURLResponse(uploadURL, objectKey, contentType)
	response.UploadQuotaMetadata = quota
	return response
}

type MomentUploadURLBatchResponse struct {
	URLs []MomentUploadURLResponse `json:"urls"`
	UploadQuotaMetadata
}

func NewMomentUploadURLBatchResponse(urls []MomentUploadURLResponse, quota UploadQuotaMetadata) MomentUploadURLBatchResponse {
	return MomentUploadURLBatchResponse{
		URLs:                urls,
		UploadQuotaMetadata: quota,
	}
}

type SharedMultipartUploadStartResponse struct {
	UploadID    string                `json:"upload_id"`
	ObjectKey   string                `json:"object_key"`
	S3Key       string                `json:"s3_key"`
	PartURLs    []PresignedUploadPart `json:"part_urls"`
	ContentType string                `json:"content_type,omitempty"`
	UploadQuotaMetadata
}

func normalizePresignedUploadParts(parts []PresignedUploadPart) []PresignedUploadPart {
	byPartNumber := make(map[int]PresignedUploadPart, len(parts))
	for _, part := range parts {
		if part.PartNumber <= 0 {
			continue
		}
		part.URL = strings.TrimSpace(part.URL)
		if part.URL == "" {
			continue
		}
		if _, exists := byPartNumber[part.PartNumber]; exists {
			continue
		}
		byPartNumber[part.PartNumber] = part
	}

	normalized := make([]PresignedUploadPart, 0, len(byPartNumber))
	for _, part := range byPartNumber {
		normalized = append(normalized, part)
	}
	sort.SliceStable(normalized, func(i, j int) bool {
		return normalized[i].PartNumber < normalized[j].PartNumber
	})
	return normalized
}

func NewSharedMultipartUploadStartResponse(uploadID, objectKey string, partURLs []PresignedUploadPart, contentType string) SharedMultipartUploadStartResponse {
	return SharedMultipartUploadStartResponse{
		UploadID:    uploadID,
		ObjectKey:   objectKey,
		S3Key:       objectKey,
		PartURLs:    normalizePresignedUploadParts(partURLs),
		ContentType: contentType,
	}
}

func NewSharedMultipartUploadStartResponseWithQuota(uploadID, objectKey string, partURLs []PresignedUploadPart, contentType string, quota UploadQuotaMetadata) SharedMultipartUploadStartResponse {
	response := NewSharedMultipartUploadStartResponse(uploadID, objectKey, partURLs, contentType)
	response.UploadQuotaMetadata = quota
	return response
}

type PublicUploadQuotaResponse struct {
	UploadsLimit     int64 `json:"uploads_limit"`
	UploadsUsed      int64 `json:"uploads_used"`
	UploadsRemaining int64 `json:"uploads_remaining"`
}

type PublicUploadLimitReachedResponse struct {
	Message          string `json:"message"`
	AlreadyUploaded  bool   `json:"already_uploaded"`
	EventName        string `json:"event_name"`
	UploadsLimit     int64  `json:"uploads_limit"`
	UploadsUsed      int64  `json:"uploads_used"`
	UploadsRemaining int64  `json:"uploads_remaining"`
}

type MomentBatchResultResponse struct {
	Succeeded int `json:"succeeded"`
	Skipped   int `json:"skipped"`
	Failed    int `json:"failed"`
}
