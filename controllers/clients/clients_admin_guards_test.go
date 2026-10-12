package clients

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"testing"

	"events-stocks/models"
	clientService "events-stocks/services/clients"
	resourceService "events-stocks/services/resources"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type roleClientRepo struct {
	mockClientRepo
	role     string
	allowed  bool
	children []models.Client
}

func (m *roleClientRepo) CheckAccessRecursive(uuid.UUID, uuid.UUID) (bool, string) {
	return m.allowed, m.role
}
func (m *roleClientRepo) GetChildrenClients(uuid.UUID) ([]models.Client, error) {
	return m.children, nil
}

type uploadRecordingStorage struct {
	mockClientLogoObjectStorage
	uploads int
}

func (s *uploadRecordingStorage) UploadRawBytesSimple([]byte, string, string, string, string, string) error {
	s.uploads++
	return nil
}

func tinyPNG(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{G: 255, A: 255})
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

func decodeErrorField(t *testing.T, body []byte) (string, string) {
	t.Helper()
	var payload struct {
		Message string `json:"message"`
		Error   string `json:"error"`
	}
	require.NoError(t, json.Unmarshal(body, &payload))
	return payload.Message, payload.Error
}

// ADM-8 regression: a non-owner PUT /clients/:id with a logo gets 403 and
// nothing is written to object storage.
func TestUpdateClient_NonOwnerWithLogo_Returns403WithoutUpload(t *testing.T) {
	orig := clientSvc
	storage := &uploadRecordingStorage{}
	rs := resourceService.NewResourceService(
		&models.Config{AwsBucketName: "events-bucket"},
		resourceService.ResourceServiceDeps{Storage: storage},
	)
	clientSvc = clientService.NewClientService(&roleClientRepo{allowed: true, role: "VIEWER"}, &mockClientRoleRepo{}, &mockClientTypeRepo{}, rs, nil, nil)
	t.Cleanup(func() { clientSvc = orig })
	setAuthenticatedClientUser(t, uuid.Must(uuid.NewV4()))

	clientID := uuid.Must(uuid.NewV4())
	c, rec := newMultipartLogoCtx(t, http.MethodPut, "/clients/"+clientID.String(), "logo.png", "image/png", tinyPNG(t))
	c.Set("cognito_sub", "client-controller-test-sub")
	c.SetParamNames("id")
	c.SetParamValues(clientID.String())

	require.NoError(t, UpdateClient(c))
	assert.Equal(t, http.StatusForbidden, rec.Code)
	_, code := decodeErrorField(t, rec.Body.Bytes())
	assert.Equal(t, ErrCodeClientPermissionDenied, code)
	assert.Zero(t, storage.uploads, "unauthorized caller must not write to storage")
}

func TestUpdateClient_OwnerWithLogo_Uploads(t *testing.T) {
	orig := clientSvc
	storage := &uploadRecordingStorage{}
	rs := resourceService.NewResourceService(
		&models.Config{AwsBucketName: "events-bucket"},
		resourceService.ResourceServiceDeps{Storage: storage},
	)
	clientSvc = clientService.NewClientService(&roleClientRepo{allowed: true, role: "OWNER"}, &mockClientRoleRepo{}, &mockClientTypeRepo{}, rs, nil, nil)
	t.Cleanup(func() { clientSvc = orig })
	setAuthenticatedClientUser(t, uuid.Must(uuid.NewV4()))

	clientID := uuid.Must(uuid.NewV4())
	c, rec := newMultipartLogoCtx(t, http.MethodPut, "/clients/"+clientID.String(), "logo.png", "image/png", tinyPNG(t))
	c.Set("cognito_sub", "client-controller-test-sub")
	c.SetParamNames("id")
	c.SetParamValues(clientID.String())

	require.NoError(t, UpdateClient(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, 1, storage.uploads)
}

// ADM-6 regression: deleting an organization with sub-organizations is a 409
// with a Spanish message and a stable code, not a 500.
func TestDeleteClient_WithChildren_Returns409WithCode(t *testing.T) {
	orig := clientSvc
	repo := &roleClientRepo{allowed: true, role: "OWNER", children: []models.Client{{ID: uuid.Must(uuid.NewV4())}}}
	clientSvc = clientService.NewClientService(repo, &mockClientRoleRepo{}, &mockClientTypeRepo{}, nil, nil, nil)
	t.Cleanup(func() { clientSvc = orig })
	setAuthenticatedClientUser(t, uuid.Must(uuid.NewV4()))

	clientID := uuid.Must(uuid.NewV4())
	c, rec := newEchoCtx(http.MethodDelete, "/clients/"+clientID.String(), "")
	c.Set("cognito_sub", "client-controller-test-sub")
	c.SetParamNames("id")
	c.SetParamValues(clientID.String())

	require.NoError(t, DeleteClient(c))
	assert.Equal(t, http.StatusConflict, rec.Code)
	message, code := decodeErrorField(t, rec.Body.Bytes())
	assert.Equal(t, ErrCodeClientHasChildren, code)
	assert.Contains(t, message, "sub-organizaciones")
}

func TestDeleteClient_NonOwner_Returns403WithCode(t *testing.T) {
	orig := clientSvc
	clientSvc = clientService.NewClientService(&roleClientRepo{allowed: true, role: "ADMIN"}, &mockClientRoleRepo{}, &mockClientTypeRepo{}, nil, nil, nil)
	t.Cleanup(func() { clientSvc = orig })
	setAuthenticatedClientUser(t, uuid.Must(uuid.NewV4()))

	clientID := uuid.Must(uuid.NewV4())
	c, rec := newEchoCtx(http.MethodDelete, "/clients/"+clientID.String(), "")
	c.Set("cognito_sub", "client-controller-test-sub")
	c.SetParamNames("id")
	c.SetParamValues(clientID.String())

	require.NoError(t, DeleteClient(c))
	assert.Equal(t, http.StatusForbidden, rec.Code)
	_, code := decodeErrorField(t, rec.Body.Bytes())
	assert.Equal(t, ErrCodeClientPermissionDenied, code)
}
