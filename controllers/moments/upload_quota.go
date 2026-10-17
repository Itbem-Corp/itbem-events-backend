package moments

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
)

// Upload quota keys (MOM-5 / SET-7). A whole venue often shares one public IP
// (venue Wi-Fi, mobile CGNAT), so the per-guest quota (EventConfig
// MaxUploadsPerGuest, default 50, over a 30-day window) is keyed, in order of
// priority, by:
//
//  1. the invitation, when the request carries a valid invitation token of
//     the event (personal upload routes, and the wall/quota read with a token);
//  2. the device, when the request carries header X-Upload-Device-Id with a
//     valid UUIDv4 the browser generated once and stored;
//  3. the public IP otherwise (missing or invalid header) — the legacy key.
//
// Device ids are self-asserted, so device-keyed uploads also count against a
// per-IP backstop of MaxUploadsPerGuest × the backstop multiplier (default 10,
// env MOMENT_UPLOAD_IP_BACKSTOP_MULTIPLIER). Invitation-keyed uploads are not
// backstopped (invitations are issued by the organizer), and IP-keyed uploads
// are already limited per IP.
const (
	UploadDeviceIDHeader                  = "X-Upload-Device-Id"
	DefaultUploadIPBackstopMultiplier     = 10
	UploadIPBackstopMultiplierEnvironment = "MOMENT_UPLOAD_IP_BACKSTOP_MULTIPLIER"

	uploadQuotaKindInvitation = "invitation"
	uploadQuotaKindDevice     = "device"
	uploadQuotaKindIP         = "ip"

	// uploadQuotaInvitationContextKey holds the invitation id a handler
	// validated for this request, so every quota read and write of the
	// request uses the same subject.
	uploadQuotaInvitationContextKey = "moments.upload_quota_invitation_id"
)

// uploadQuotaSubject is whose counter an upload request moves.
type uploadQuotaSubject struct {
	eventID     string
	kind        string
	key         string
	backstopKey string // set only for device-keyed uploads
}

// uploadLimitKey is the legacy per-IP counter key; IP-keyed uploads keep it so
// counters already in Valkey stay valid across the deploy.
func uploadLimitKey(eventID, ip string) string {
	return fmt.Sprintf("moments:upload_count:%s:%s", eventID, ip)
}

func uploadInvitationLimitKey(eventID string, invitationID uuid.UUID) string {
	return fmt.Sprintf("moments:upload_count:%s:inv:%s", eventID, invitationID)
}

func uploadDeviceLimitKey(eventID, deviceID string) string {
	return fmt.Sprintf("moments:upload_count:%s:dev:%s", eventID, deviceID)
}

func uploadIPBackstopKey(eventID, ip string) string {
	return fmt.Sprintf("moments:upload_count:%s:ipcap:%s", eventID, ip)
}

// setUploadQuotaInvitation records the invitation a handler validated.
func setUploadQuotaInvitation(c echo.Context, invitationID *uuid.UUID) {
	if c != nil && invitationID != nil && *invitationID != uuid.Nil {
		c.Set(uploadQuotaInvitationContextKey, *invitationID)
	}
}

// uploadDeviceID reads X-Upload-Device-Id. Only a canonical-form RFC 4122
// version 4 UUID is accepted; anything else falls back to the IP.
func uploadDeviceID(c echo.Context) (string, bool) {
	raw := strings.TrimSpace(c.Request().Header.Get(UploadDeviceIDHeader))
	if len(raw) != 36 {
		return "", false
	}
	id, err := uuid.FromString(raw)
	if err != nil || id.Version() != uuid.V4 || id.Variant() != uuid.VariantRFC4122 {
		return "", false
	}
	return id.String(), true
}

// uploadQuotaSubjectFor picks the quota key of this request.
func uploadQuotaSubjectFor(c echo.Context, eventID string) uploadQuotaSubject {
	ip := c.RealIP()
	if invitationID, ok := c.Get(uploadQuotaInvitationContextKey).(uuid.UUID); ok && invitationID != uuid.Nil {
		return uploadQuotaSubject{eventID: eventID, kind: uploadQuotaKindInvitation, key: uploadInvitationLimitKey(eventID, invitationID)}
	}
	if deviceID, ok := uploadDeviceID(c); ok {
		return uploadQuotaSubject{
			eventID:     eventID,
			kind:        uploadQuotaKindDevice,
			key:         uploadDeviceLimitKey(eventID, deviceID),
			backstopKey: uploadIPBackstopKey(eventID, ip),
		}
	}
	return uploadQuotaSubject{eventID: eventID, kind: uploadQuotaKindIP, key: uploadLimitKey(eventID, ip)}
}

// uploadIPBackstopMultiplier reads MOMENT_UPLOAD_IP_BACKSTOP_MULTIPLIER; a
// missing, non-numeric or < 1 value means the default (10).
func uploadIPBackstopMultiplier() int {
	if raw := strings.TrimSpace(os.Getenv(UploadIPBackstopMultiplierEnvironment)); raw != "" {
		if value, err := strconv.Atoi(raw); err == nil && value >= 1 {
			return value
		}
	}
	return DefaultUploadIPBackstopMultiplier
}

// uploadIPBackstopLimit is the per-IP cap on device-keyed uploads of an event.
func uploadIPBackstopLimit(maxUploads int) int64 {
	return int64(effectiveMaxUploads(maxUploads)) * int64(uploadIPBackstopMultiplier())
}

func readUploadCounter(ctx context.Context, key string) int64 {
	if publicUploadCounter == nil || key == "" {
		return 0
	}
	countStr, _ := publicUploadCounter.GetKey(ctx, key)
	var count int64
	fmt.Sscanf(countStr, "%d", &count)
	if count < 0 {
		return 0
	}
	return count
}

// uploadSubjectUsage returns the subject's used count and how many uploads it
// has left, the backstop included.
func uploadSubjectUsage(ctx context.Context, subject uploadQuotaSubject, maxUploads int) (used, remaining int64) {
	limit := int64(effectiveMaxUploads(maxUploads))
	used = readUploadCounter(ctx, subject.key)
	remaining = limit - used
	if subject.backstopKey != "" {
		if backstopRemaining := uploadIPBackstopLimit(maxUploads) - readUploadCounter(ctx, subject.backstopKey); backstopRemaining < remaining {
			remaining = backstopRemaining
		}
	}
	if remaining < 0 {
		remaining = 0
	}
	return used, remaining
}

func incrementUploadCounter(ctx context.Context, key string) (int64, error) {
	count, err := publicUploadCounter.Increment(ctx, key)
	if err != nil {
		return 0, err
	}
	if count == 1 {
		_ = publicUploadCounter.Expire(ctx, key, uploadWindowDays*24*time.Hour)
	}
	return count, nil
}

// checkAndIncrementUploadLimit reserves one upload for the subject.
// maxUploads is the per-event configured limit; pass 0 to use the global
// default. Returns (limitReached, usedCount, quotaReserved, error); when the
// backstop is what is exhausted, usedCount is reported as the limit so the
// 429 payload shows nothing remaining. If Valkey is unavailable the upload
// is allowed.
func checkAndIncrementUploadLimit(ctx context.Context, subject uploadQuotaSubject, maxUploads int) (bool, int64, bool, error) {
	maxUploads = effectiveMaxUploads(maxUploads)
	if publicUploadCounter == nil {
		return false, 0, false, nil
	}
	count, err := incrementUploadCounter(ctx, subject.key)
	if err != nil {
		return false, 0, false, nil
	}
	reached := count > int64(maxUploads)
	if subject.backstopKey != "" {
		backstop, backstopErr := incrementUploadCounter(ctx, subject.backstopKey)
		if backstopErr == nil && backstop > uploadIPBackstopLimit(maxUploads) && !reached {
			reached = true
			slog.Warn("moment upload per-IP backstop reached", "event_id", subject.eventID)
			count = int64(maxUploads)
		}
	}
	return reached, count, true, nil
}

// rollbackUploadLimit gives back a reservation made by
// checkAndIncrementUploadLimit (both counters for a device subject).
func rollbackUploadLimit(subject uploadQuotaSubject) {
	if publicUploadCounter == nil || subject.key == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, key := range []string{subject.key, subject.backstopKey} {
		if key == "" {
			continue
		}
		if _, err := publicUploadCounter.Decrement(ctx, key); err != nil {
			slog.Error("moment upload quota rollback failed", "event_id", subject.eventID, "kind", subject.kind, "error", err)
		}
	}
}

// refundFailedMomentUpload gives the slot back when the moment could not be
// queued for processing (it is stored as "failed" and never reaches the wall),
// so a failed upload does not use up the guest's quota (STU-66). Failures the
// media worker reports later through the callback are not refunded: the
// quota subject (device/IP key) is not stored on the moment.
func refundFailedMomentUpload(subject uploadQuotaSubject, quotaReserved bool) {
	if quotaReserved {
		rollbackUploadLimit(subject)
	}
}
