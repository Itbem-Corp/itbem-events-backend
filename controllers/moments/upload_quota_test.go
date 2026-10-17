package moments

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"testing"
	"time"

	"events-stocks/dtos"
	"events-stocks/models"
	momentsService "events-stocks/services/moments"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// keyedUploadCounter is a Valkey stand-in that keeps one counter per key.
type keyedUploadCounter struct {
	values map[string]int64
}

func newKeyedUploadCounter() *keyedUploadCounter {
	return &keyedUploadCounter{values: map[string]int64{}}
}

func (m *keyedUploadCounter) GetKey(_ context.Context, key string) (string, error) {
	return strconv.FormatInt(m.values[key], 10), nil
}
func (m *keyedUploadCounter) Increment(_ context.Context, key string) (int64, error) {
	m.values[key]++
	return m.values[key], nil
}
func (m *keyedUploadCounter) Decrement(_ context.Context, key string) (int64, error) {
	m.values[key]--
	return m.values[key], nil
}
func (m *keyedUploadCounter) Expire(context.Context, string, time.Duration) error { return nil }

func useKeyedCounter(t *testing.T) *keyedUploadCounter {
	t.Helper()
	counter := newKeyedUploadCounter()
	orig := publicUploadCounter
	publicUploadCounter = counter
	t.Cleanup(func() { publicUploadCounter = orig })
	return counter
}

// uploadCtx is a request from ip, optionally with an X-Upload-Device-Id.
func uploadCtx(ip, deviceID string) echo.Context {
	c, _ := newEchoCtx(http.MethodPost, "/upload", "")
	c.Request().Header.Set(echo.HeaderXRealIP, ip)
	if deviceID != "" {
		c.Request().Header.Set(UploadDeviceIDHeader, deviceID)
	}
	return c
}

func newDeviceID() string { return uuid.Must(uuid.NewV4()).String() }

// MOM-5 / SET-7 contract: invitation > valid UUIDv4 device header > IP.
func TestUploadQuotaSubject_PriorityInvitationThenDeviceThenIP(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4()).String()
	device := newDeviceID()

	c := uploadCtx("203.0.113.7", device)
	invitationID := uuid.Must(uuid.NewV4())
	setUploadQuotaInvitation(c, &invitationID)
	subject := uploadQuotaSubjectFor(c, eventID)
	assert.Equal(t, uploadQuotaKindInvitation, subject.kind)
	assert.Equal(t, "moments:upload_count:"+eventID+":inv:"+invitationID.String(), subject.key)
	assert.Empty(t, subject.backstopKey, "invitations are not backstopped")

	subject = uploadQuotaSubjectFor(uploadCtx("203.0.113.7", device), eventID)
	assert.Equal(t, uploadQuotaKindDevice, subject.kind)
	assert.Equal(t, "moments:upload_count:"+eventID+":dev:"+device, subject.key)
	assert.Equal(t, "moments:upload_count:"+eventID+":ipcap:203.0.113.7", subject.backstopKey)

	// Uppercase is the same device.
	upper := uploadQuotaSubjectFor(uploadCtx("203.0.113.7", strUpper(device)), eventID)
	assert.Equal(t, subject.key, upper.key)

	for _, invalid := range []string{"", "not-a-uuid", "6ba7b810-9dad-11d1-80b4-00c04fd430c8" /* v1 */, "{" + device + "}", device + " x", "00000000-0000-0000-0000-000000000000"} {
		subject = uploadQuotaSubjectFor(uploadCtx("203.0.113.7", invalid), eventID)
		assert.Equal(t, uploadQuotaKindIP, subject.kind, invalid)
		assert.Equal(t, "moments:upload_count:"+eventID+":203.0.113.7", subject.key, "legacy per-IP key")
	}
}

func strUpper(value string) string {
	out := []byte(value)
	for i, ch := range out {
		if ch >= 'a' && ch <= 'z' {
			out[i] = ch - 32
		}
	}
	return string(out)
}

// A whole venue on one IP: every device keeps its own quota.
func TestUploadQuota_DevicesBehindOneIPHaveTheirOwnQuota(t *testing.T) {
	counter := useKeyedCounter(t)
	eventID := uuid.Must(uuid.NewV4()).String()
	const perDevice = 3
	for device := 0; device < 5; device++ {
		c := uploadCtx("198.51.100.1", newDeviceID())
		for upload := 1; upload <= perDevice; upload++ {
			reached, _, reserved, _ := checkAndIncrementUploadLimit(context.Background(), uploadQuotaSubjectFor(c, eventID), perDevice)
			require.False(t, reached, "device %d upload %d", device, upload)
			require.True(t, reserved)
		}
		reached, _, _, _ := checkAndIncrementUploadLimit(context.Background(), uploadQuotaSubjectFor(c, eventID), perDevice)
		assert.True(t, reached, "the 4th upload of one device is over its quota")
		rollbackUploadLimit(uploadQuotaSubjectFor(c, eventID))
	}
	assert.Equal(t, int64(15), counter.values["moments:upload_count:"+eventID+":ipcap:198.51.100.1"])
}

// The per-IP backstop (limit × multiplier) stops fake device ids, rolls back
// both counters, and reports nothing remaining.
func TestUploadQuota_IPBackstopStopsRotatingDeviceIDs(t *testing.T) {
	t.Setenv(UploadIPBackstopMultiplierEnvironment, "2")
	counter := useKeyedCounter(t)
	eventID := uuid.Must(uuid.NewV4()).String()
	const perDevice = 3 // backstop 6
	uploads := 0
	var lastReached bool
	var lastCount int64
	var last uploadQuotaSubject
	for i := 0; i < 7; i++ {
		last = uploadQuotaSubjectFor(uploadCtx("198.51.100.9", newDeviceID()), eventID)
		lastReached, lastCount, _, _ = checkAndIncrementUploadLimit(context.Background(), last, perDevice)
		if !lastReached {
			uploads++
		}
	}
	assert.Equal(t, 6, uploads)
	assert.True(t, lastReached)
	assert.Equal(t, int64(perDevice), lastCount, "reported as the full limit")
	rollbackUploadLimit(last)
	assert.Equal(t, int64(6), counter.values[last.backstopKey])
	assert.Zero(t, counter.values[last.key])

	// The read side agrees: a new device behind that IP has nothing left.
	c := uploadCtx("198.51.100.9", newDeviceID())
	quota := getUploadQuota(c, uuid.FromStringOrNil(eventID), &models.EventConfig{MaxUploadsPerGuest: perDevice})
	assert.Equal(t, int64(0), quota.Remaining)
	ok, _ := ensureUploadSlots(c, &models.Event{ID: uuid.FromStringOrNil(eventID), Name: "Boda"}, &models.EventConfig{MaxUploadsPerGuest: perDevice}, 1)
	assert.False(t, ok)
}

func TestUploadQuota_BackstopMultiplierEnvironment(t *testing.T) {
	t.Setenv(UploadIPBackstopMultiplierEnvironment, "")
	assert.Equal(t, int64(300), uploadIPBackstopLimit(30))
	assert.Equal(t, int64(500), uploadIPBackstopLimit(0), "the default per-guest limit is 50")
	t.Setenv(UploadIPBackstopMultiplierEnvironment, "25")
	assert.Equal(t, int64(750), uploadIPBackstopLimit(30))
	for _, invalid := range []string{"0", "-3", "abc"} {
		t.Setenv(UploadIPBackstopMultiplierEnvironment, invalid)
		assert.Equal(t, int64(300), uploadIPBackstopLimit(30), invalid)
	}
}

// A personal confirm counts against the invitation, not the IP or device.
func TestConfirmPresignedMoment_PersonalUploadCountsAgainstTheInvitation(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	invitationID := uuid.Must(uuid.NewV4())
	key := "moments/" + eventID.String() + "/raw/photo.jpg"
	repo := &mockMomentRepo{
		GetByEventAndContentURLFunc: func(uuid.UUID, string) (*models.Moment, error) { return nil, gorm.ErrRecordNotFound },
	}
	setupConfirmMomentTest(t, repo, &mockObjectStorage{}, &mockUploadCounter{})
	counter := useKeyedCounter(t)
	c := uploadCtx("198.51.100.20", newDeviceID())

	_, err := confirmPresignedMoment(c, &models.Event{ID: eventID}, &models.EventConfig{MaxUploadsPerGuest: 5}, &invitationID, key, "image/jpeg", 0, "")
	require.NoError(t, err)
	assert.Equal(t, map[string]int64{"moments:upload_count:" + eventID.String() + ":inv:" + invitationID.String(): 1}, counter.values)
}

// STU-66: when the moment cannot be queued for processing (stored as failed),
// the reserved quota slot is given back.
func TestConfirmPresignedMoment_EnqueueFailureRefundsQuota(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	invitationID := uuid.Must(uuid.NewV4())
	key := "moments/" + eventID.String() + "/raw/photo.jpg"
	repo := &mockMomentRepo{
		GetByEventAndContentURLFunc: func(uuid.UUID, string) (*models.Moment, error) { return nil, gorm.ErrRecordNotFound },
	}
	setupConfirmMomentTest(t, repo, &mockObjectStorage{}, &mockUploadCounter{})
	momentsService.SetDefaultMomentService(momentsService.NewMomentService(repo, &mockCacheRepo{}, &mockMediaPublisher{
		PublishFunc: func(dtos.MediaProcessMessage) (bool, error) { return false, errors.New("queue down") },
	}))
	counter := useKeyedCounter(t)
	c := uploadCtx("198.51.100.20", newDeviceID())

	_, err := confirmPresignedMoment(c, &models.Event{ID: eventID}, &models.EventConfig{MaxUploadsPerGuest: 5}, &invitationID, key, "image/jpeg", 0, "")
	require.NoError(t, err)
	assert.Equal(t, map[string]int64{"moments:upload_count:" + eventID.String() + ":inv:" + invitationID.String(): 0}, counter.values)
}
