package clients

import (
	"bytes"
	"errors"
	"testing"

	"events-stocks/models"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingLogoStorage counts uploads so a test can prove that nothing was
// written to object storage.
type recordingLogoStorage struct {
	clientTestLogoStorage
	uploads []string
}

func (s *recordingLogoStorage) UploadRawBytesSimple(_ []byte, filename, _, folder, _, _ string) error {
	s.uploads = append(s.uploads, folder+"/"+filename)
	return s.uploadErr
}

func newLogoUpdateService(repo *mockClientRepo, storage *recordingLogoStorage) *ClientService {
	return newClientCreateWithLogoService(repo, storage)
}

// ADM-8: a caller without OWNER access must not be able to write objects
// under organizations/<id>/branding/logo/.
func TestUpdateClientWithLogo_NonOwnerNeverUploads(t *testing.T) {
	storage := &recordingLogoStorage{}
	repo := &mockClientRepo{
		CheckAccessRecursiveFunc: func(uuid.UUID, uuid.UUID) (bool, string) { return true, "ADMIN" },
	}
	svc := newLogoUpdateService(repo, storage)
	payload := validClientLogoPNG(t)

	_, err := svc.UpdateClientWithLogo(
		uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), false, "Name",
		&clientTestMultipartFile{Reader: bytes.NewReader(payload)},
		clientLogoFileHeader("logo.png", "image/png", int64(len(payload))),
		false,
	)

	require.ErrorIs(t, err, ErrClientPermissionDenied)
	assert.Empty(t, storage.uploads, "no object may be written before authorization")
}

func TestUpdateClientWithLogo_NonMemberNeverUploads(t *testing.T) {
	storage := &recordingLogoStorage{}
	svc := newLogoUpdateService(&mockClientRepo{}, storage) // default: no access
	payload := validClientLogoPNG(t)

	_, err := svc.UpdateClientWithLogo(
		uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), false, "",
		&clientTestMultipartFile{Reader: bytes.NewReader(payload)},
		clientLogoFileHeader("logo.png", "image/png", int64(len(payload))),
		false,
	)

	require.ErrorIs(t, err, ErrClientPermissionDenied)
	assert.Empty(t, storage.uploads)
}

func TestUpdateClientWithLogo_MissingClientNeverUploads(t *testing.T) {
	storage := &recordingLogoStorage{}
	repo := &mockClientRepo{
		CheckAccessRecursiveFunc: func(uuid.UUID, uuid.UUID) (bool, string) { return true, "OWNER" },
		GetClientByIDFunc:        func(uuid.UUID) (*models.Client, error) { return nil, errors.New("record not found") },
	}
	svc := newLogoUpdateService(repo, storage)
	payload := validClientLogoPNG(t)

	_, err := svc.UpdateClientWithLogo(
		uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), false, "",
		&clientTestMultipartFile{Reader: bytes.NewReader(payload)},
		clientLogoFileHeader("logo.png", "image/png", int64(len(payload))),
		false,
	)

	require.ErrorIs(t, err, ErrClientNotFound)
	assert.Empty(t, storage.uploads)
}

func TestUpdateClientWithLogo_OwnerUploadsAndSaves(t *testing.T) {
	clientID := uuid.Must(uuid.NewV4())
	storage := &recordingLogoStorage{}
	var saved *models.Client
	repo := &mockClientRepo{
		CheckAccessRecursiveFunc: func(uuid.UUID, uuid.UUID) (bool, string) { return true, "INHERITED_OWNER" },
		GetClientByIDFunc: func(id uuid.UUID) (*models.Client, error) {
			return &models.Client{ID: id, Name: "Old"}, nil
		},
		UpdateClientFunc: func(c *models.Client) error { saved = c; return nil },
	}
	svc := newLogoUpdateService(repo, storage)
	payload := validClientLogoPNG(t)

	res, err := svc.UpdateClientWithLogo(
		clientID, uuid.Must(uuid.NewV4()), false, "New",
		&clientTestMultipartFile{Reader: bytes.NewReader(payload)},
		clientLogoFileHeader("logo.png", "image/png", int64(len(payload))),
		false,
	)

	require.NoError(t, err)
	require.Len(t, storage.uploads, 1)
	assert.Contains(t, storage.uploads[0], "organizations/"+clientID.String()+"/branding/logo/")
	require.NotNil(t, saved)
	assert.Equal(t, "New", res.Name)
	assert.Contains(t, res.Logo, "organizations/"+clientID.String()+"/branding/logo/")
	assert.Empty(t, storage.deleted)
}

func TestUpdateClientWithLogo_CleansUpNewObjectWhenSaveFails(t *testing.T) {
	clientID := uuid.Must(uuid.NewV4())
	storage := &recordingLogoStorage{}
	repo := &mockClientRepo{
		CheckAccessRecursiveFunc: func(uuid.UUID, uuid.UUID) (bool, string) { return true, "OWNER" },
		GetClientByIDFunc: func(id uuid.UUID) (*models.Client, error) {
			return &models.Client{ID: id}, nil
		},
		UpdateClientFunc: func(*models.Client) error { return errors.New("database unavailable") },
	}
	svc := newLogoUpdateService(repo, storage)
	payload := validClientLogoPNG(t)

	_, err := svc.UpdateClientWithLogo(
		clientID, uuid.Must(uuid.NewV4()), false, "",
		&clientTestMultipartFile{Reader: bytes.NewReader(payload)},
		clientLogoFileHeader("logo.png", "image/png", int64(len(payload))),
		false,
	)

	require.Error(t, err)
	require.Len(t, storage.uploads, 1)
	require.Len(t, storage.deleted, 1, "the uploaded object must be removed exactly once")
	assert.Equal(t, storage.uploads[0], storage.deleted[0])
}

func TestUpdateClientWithLogo_CleansUpWhenAccessRevokedAfterUpload(t *testing.T) {
	clientID := uuid.Must(uuid.NewV4())
	storage := &recordingLogoStorage{}
	calls := 0
	repo := &mockClientRepo{
		CheckAccessRecursiveFunc: func(uuid.UUID, uuid.UUID) (bool, string) {
			calls++
			if calls == 1 {
				return true, "OWNER"
			}
			return false, "" // revoked between the check and the update
		},
		GetClientByIDFunc: func(id uuid.UUID) (*models.Client, error) { return &models.Client{ID: id}, nil },
	}
	svc := newLogoUpdateService(repo, storage)
	payload := validClientLogoPNG(t)

	_, err := svc.UpdateClientWithLogo(
		clientID, uuid.Must(uuid.NewV4()), false, "",
		&clientTestMultipartFile{Reader: bytes.NewReader(payload)},
		clientLogoFileHeader("logo.png", "image/png", int64(len(payload))),
		false,
	)

	require.ErrorIs(t, err, ErrClientPermissionDenied)
	require.Len(t, storage.uploads, 1)
	require.Len(t, storage.deleted, 1)
	assert.Equal(t, storage.uploads[0], storage.deleted[0])
}

// ADM-6: children → stable sentinel (mapped to 409), and the logo object is
// deleted only after the rows were deleted.
func TestDeleteClient_WithChildrenReturnsSentinelAndKeepsLogo(t *testing.T) {
	storage := &recordingLogoStorage{}
	repo := &mockClientRepo{
		CheckAccessRecursiveFunc: func(uuid.UUID, uuid.UUID) (bool, string) { return true, "OWNER" },
		GetChildrenClientsFunc: func(uuid.UUID) ([]models.Client, error) {
			return []models.Client{{ID: uuid.Must(uuid.NewV4())}}, nil
		},
		GetClientByIDFunc: func(id uuid.UUID) (*models.Client, error) {
			return &models.Client{ID: id, Logo: "organizations/x/branding/logo/a.webp"}, nil
		},
	}
	svc := newLogoUpdateService(repo, storage)

	err := svc.DeleteClient(uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()))

	require.ErrorIs(t, err, ErrClientHasChildren)
	assert.Empty(t, storage.deleted)
}

func TestDeleteClient_LogoKeptWhenRowDeleteFails(t *testing.T) {
	storage := &recordingLogoStorage{}
	repo := &mockClientRepo{
		CheckAccessRecursiveFunc: func(uuid.UUID, uuid.UUID) (bool, string) { return true, "OWNER" },
		GetClientByIDFunc: func(id uuid.UUID) (*models.Client, error) {
			return &models.Client{ID: id, Logo: "organizations/x/branding/logo/a.webp"}, nil
		},
		DeleteClientFunc: func(uuid.UUID) error { return errors.New("fk violation") },
	}
	svc := newLogoUpdateService(repo, storage)

	err := svc.DeleteClient(uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()))

	require.Error(t, err)
	assert.Empty(t, storage.deleted, "logo must survive a failed delete")
}

func TestDeleteClient_LogoDeletedAfterRows(t *testing.T) {
	storage := &recordingLogoStorage{}
	rowsDeleted := false
	repo := &mockClientRepo{
		CheckAccessRecursiveFunc: func(uuid.UUID, uuid.UUID) (bool, string) { return true, "OWNER" },
		GetClientByIDFunc: func(id uuid.UUID) (*models.Client, error) {
			return &models.Client{ID: id, Logo: "organizations/x/branding/logo/a.webp"}, nil
		},
		DeleteClientFunc: func(uuid.UUID) error {
			assert.Empty(t, storage.deleted, "logo deleted before the row")
			rowsDeleted = true
			return nil
		},
	}
	svc := newLogoUpdateService(repo, storage)

	require.NoError(t, svc.DeleteClient(uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())))
	assert.True(t, rowsDeleted)
	require.Len(t, storage.deleted, 1)
	assert.Equal(t, "organizations/x/branding/logo/a.webp", storage.deleted[0])
}
