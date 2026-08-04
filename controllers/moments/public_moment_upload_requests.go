package moments

import "events-stocks/dtos"

// momentUploadTokenRequest accepts the historic token field spellings used by
// public upload clients while presenting one canonical accessor to handlers.
type momentUploadTokenRequest struct {
	PrettyToken           string `json:"pretty_token"`
	PrettyTokenCamel      string `json:"prettyToken"`
	PrettyTokenPascal     string `json:"PrettyToken"`
	InvitationToken       string `json:"invitation_token"`
	InvitationTokenCamel  string `json:"invitationToken"`
	InvitationTokenPascal string `json:"InvitationToken"`
	Token                 string `json:"token"`
	TokenPascal           string `json:"Token"`
}

func (r momentUploadTokenRequest) invitationToken() string {
	return publicMomentInvitationToken(
		r.PrettyToken,
		r.PrettyTokenCamel,
		r.PrettyTokenPascal,
		r.InvitationToken,
		r.InvitationTokenCamel,
		r.InvitationTokenPascal,
		r.Token,
		r.TokenPascal,
	)
}

// momentUploadFileRequest accepts snake_case, camelCase and PascalCase fields
// from older public clients. Handlers consume only its canonical accessors.
type momentUploadFileRequest struct {
	ContentType       string `json:"content_type"`
	ContentTypeCamel  string `json:"contentType"`
	ContentTypePascal string `json:"ContentType"`
	Filename          string `json:"filename"`
	FilenameCamel     string `json:"fileName"`
	FilenamePascal    string `json:"FileName"`
	FileSize          int64  `json:"file_size"`
	FileSizeCamel     int64  `json:"fileSize"`
	FileSizePascal    int64  `json:"FileSize"`
}

func (r momentUploadFileRequest) filename() string {
	return firstNonEmpty(r.Filename, r.FilenameCamel, r.FilenamePascal)
}

func (r momentUploadFileRequest) contentType() string {
	return firstNonEmpty(r.ContentType, r.ContentTypeCamel, r.ContentTypePascal)
}

func (r momentUploadFileRequest) fileSize() int64 {
	return firstNonZeroInt64(r.FileSize, r.FileSizeCamel, r.FileSizePascal)
}

type momentUploadObjectRequest struct {
	ObjectKey         string `json:"object_key"`
	ObjectKeyCamel    string `json:"objectKey"`
	ObjectKeyPascal   string `json:"ObjectKey"`
	S3Key             string `json:"s3_key"`
	S3KeyCamel        string `json:"s3Key"`
	S3KeyPascal       string `json:"S3Key"`
	ContentType       string `json:"content_type"`
	ContentTypeCamel  string `json:"contentType"`
	ContentTypePascal string `json:"ContentType"`
	Description       string `json:"description"`
	FileSize          int64  `json:"file_size"`
	FileSizeCamel     int64  `json:"fileSize"`
	FileSizePascal    int64  `json:"FileSize"`
}

func (r momentUploadObjectRequest) objectKey() string {
	return uploadObjectKey(r.ObjectKey, r.ObjectKeyCamel, r.ObjectKeyPascal, r.S3Key, r.S3KeyCamel, r.S3KeyPascal)
}

func (r momentUploadObjectRequest) contentType() string {
	return firstNonEmpty(r.ContentType, r.ContentTypeCamel, r.ContentTypePascal)
}

func (r momentUploadObjectRequest) fileSize() int64 {
	return firstNonZeroInt64(r.FileSize, r.FileSizeCamel, r.FileSizePascal)
}

type multipartUploadReferenceRequest struct {
	UploadID       string `json:"upload_id"`
	UploadIDCamel  string `json:"uploadId"`
	UploadIDPascal string `json:"UploadID"`
}

func (r multipartUploadReferenceRequest) uploadID() string {
	return firstNonEmpty(r.UploadID, r.UploadIDCamel, r.UploadIDPascal)
}

type publicMomentUploadURLRequest struct {
	momentUploadTokenRequest
	momentUploadFileRequest
}

type publicMomentConfirmRequest struct {
	momentUploadTokenRequest
	momentUploadObjectRequest
}

type sharedUploadURLRequest = momentUploadFileRequest

type sharedBatchUploadURLRequest struct {
	Files []momentUploadFileRequest `json:"files"`
}

type sharedMomentConfirmRequest = momentUploadObjectRequest

type sharedMultipartStartRequest struct {
	momentUploadFileRequest
}

type sharedMultipartAbortRequest struct {
	multipartUploadReferenceRequest
	momentUploadObjectRequest
}

type sharedMultipartCompleteRequest struct {
	multipartUploadReferenceRequest
	momentUploadObjectRequest
	Parts              []dtos.CompletedUploadPart `json:"parts"`
	PartsPascal        []dtos.CompletedUploadPart `json:"Parts"`
	CompletedParts     []dtos.CompletedUploadPart `json:"completed_parts"`
	CompletedPartsAlt  []dtos.CompletedUploadPart `json:"completedParts"`
	CompletedPartsCaps []dtos.CompletedUploadPart `json:"CompletedParts"`
}

func (r sharedMultipartCompleteRequest) completedParts() []dtos.CompletedUploadPart {
	switch {
	case len(r.Parts) > 0:
		return r.Parts
	case len(r.PartsPascal) > 0:
		return r.PartsPascal
	case len(r.CompletedParts) > 0:
		return r.CompletedParts
	case len(r.CompletedPartsAlt) > 0:
		return r.CompletedPartsAlt
	default:
		return r.CompletedPartsCaps
	}
}
