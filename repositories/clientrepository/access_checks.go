package clientrepository

import (
	"fmt"
	"strings"

	"events-stocks/repositories/gormrepository"

	"github.com/gofrs/uuid"
	"gorm.io/gorm"
)

// organizationAccessSQL resolves the nearest active membership of a user in
// the target organization or one of its ancestors. The walk is bounded to 32
// levels, like requireTenantClientBoundary, so a corrupt cyclic hierarchy
// terminates.
const organizationAccessSQL = `
	WITH RECURSIVE anc AS (
		SELECT id, parent_id, 0 AS level
		FROM clients
		WHERE id = @client AND deleted_at IS NULL
		UNION ALL
		SELECT c.id, c.parent_id, a.level + 1
		FROM clients c
		INNER JOIN anc a ON c.id = a.parent_id
		WHERE c.deleted_at IS NULL AND a.level < 31
	)
	SELECT cr.code AS role_code, anc.level
	FROM anc
	JOIN client_members cm ON cm.client_id = anc.id
		AND cm.user_id = @user
		AND cm.is_active = true
	JOIN client_roles cr ON cr.id = cm.client_role_id
	ORDER BY anc.level ASC
	LIMIT 1
`

// applicationAccessSQL answers the organization lookup and the product
// entitlement in one query. The product of an organization is its nearest
// branded ancestor (an ancestor whose code is an active application code):
// the ITBEM and Cafetton roots are children of the EventiApp root, so the
// nearest one wins.
const applicationAccessSQL = `
	WITH RECURSIVE anc AS (
		SELECT id, parent_id, code, is_active, 0 AS level
		FROM clients
		WHERE id = @client AND deleted_at IS NULL
		UNION ALL
		SELECT c.id, c.parent_id, c.code, c.is_active, anc.level + 1
		FROM clients c
		JOIN anc ON c.id = anc.parent_id
		WHERE c.deleted_at IS NULL AND anc.level < 31
	),
	branded AS (
		SELECT anc.id AS root_id, ap.id AS application_id, LOWER(ap.code) AS product
		FROM anc
		JOIN applications ap ON LOWER(ap.code) = LOWER(anc.code) AND ap.is_active = true
		ORDER BY anc.level
		LIMIT 1
	),
	membership AS (
		SELECT cm.id AS member_id, cr.code AS role_code, anc.level
		FROM anc
		JOIN client_members cm ON cm.client_id = anc.id AND cm.user_id = @user AND cm.is_active = true
		JOIN client_roles cr ON cr.id = cm.client_role_id
		ORDER BY anc.level
		LIMIT 1
	)
	SELECT
		m.role_code,
		m.level,
		COALESCE((SELECT is_active FROM anc WHERE level = 0), false) AS target_active,
		COALESCE((SELECT product FROM branded), '') AS target_product,
		EXISTS (
			SELECT 1 FROM branded b
			JOIN client_applications ca ON ca.client_id = b.root_id
				AND ca.application_id = b.application_id
				AND ca.is_active = true
		) AS product_enabled,
		EXISTS (
			SELECT 1 FROM client_member_applications cma
			JOIN applications ap ON ap.id = cma.application_id AND ap.is_active = true
			WHERE cma.client_member_id = m.member_id
				AND cma.is_active = true
				AND LOWER(ap.code) = @app
		) AS member_entitled
	FROM membership m
`

// ApplicationAccess is the organization lookup plus the product entitlement
// of one (user, organization) pair.
type ApplicationAccess struct {
	Allowed  bool
	Role     string
	Entitled bool
	// Reason is the first failed entitlement condition, empty when entitled.
	Reason string
}

type organizationAccessRow struct {
	RoleCode string
	Level    int
}

type applicationAccessRow struct {
	RoleCode       string
	Level          int
	TargetActive   bool
	TargetProduct  string
	ProductEnabled bool
	MemberEntitled bool
}

// CheckAccessRecursiveE is CheckAccessRecursive with the database error
// returned instead of swallowed. Authorization must turn an error into a 500,
// never into "no access" that could fall through to another authority path.
func CheckAccessRecursiveE(userID, targetClientID uuid.UUID) (bool, string, error) {
	return checkAccessRecursiveE(gormrepository.DB(), userID, targetClientID)
}

func checkAccessRecursiveE(db *gorm.DB, userID, targetClientID uuid.UUID) (bool, string, error) {
	var row organizationAccessRow
	if err := db.Raw(organizationAccessSQL, map[string]any{"client": targetClientID, "user": userID}).Scan(&row).Error; err != nil {
		return false, "", fmt.Errorf("check organization access: %w", err)
	}
	allowed, role := resolveOrganizationRole(row.RoleCode, row.Level)
	return allowed, role, nil
}

// resolveOrganizationRole applies the inheritance rule: a direct membership
// keeps its role; an ancestor membership propagates only Owner/Admin, as
// INHERITED_<ROLE>.
func resolveOrganizationRole(roleCode string, level int) (bool, string) {
	if roleCode == "" {
		return false, ""
	}
	if level == 0 {
		return true, roleCode
	}
	roleUpper := strings.ToUpper(roleCode)
	if roleUpper == "OWNER" || roleUpper == "ADMIN" {
		return true, "INHERITED_" + roleUpper
	}
	return false, ""
}

// CheckApplicationAccessRecursive answers CheckAccessRecursiveE plus whether
// the membership is entitled to application on the target organization:
// the organization is active, its nearest branded ancestor is application,
// the product is enabled on that branded root, and the member has an active
// client_member_applications row for it.
func CheckApplicationAccessRecursive(userID, targetClientID uuid.UUID, application string) (ApplicationAccess, error) {
	return checkApplicationAccessRecursive(gormrepository.DB(), userID, targetClientID, application)
}

func checkApplicationAccessRecursive(db *gorm.DB, userID, targetClientID uuid.UUID, application string) (ApplicationAccess, error) {
	application = strings.ToLower(strings.TrimSpace(application))
	var row applicationAccessRow
	if err := db.Raw(applicationAccessSQL, map[string]any{
		"client": targetClientID,
		"user":   userID,
		"app":    application,
	}).Scan(&row).Error; err != nil {
		return ApplicationAccess{}, fmt.Errorf("check application access: %w", err)
	}
	return evaluateApplicationAccess(row, application), nil
}

func evaluateApplicationAccess(row applicationAccessRow, application string) ApplicationAccess {
	allowed, role := resolveOrganizationRole(row.RoleCode, row.Level)
	access := ApplicationAccess{Allowed: allowed, Role: role}
	if !allowed {
		return access
	}
	switch {
	case !row.TargetActive:
		access.Reason = "organization_inactive"
	case !strings.EqualFold(row.TargetProduct, application):
		access.Reason = "product_mismatch"
	case !row.ProductEnabled:
		access.Reason = "product_disabled"
	case !row.MemberEntitled:
		access.Reason = "member_not_entitled"
	default:
		access.Entitled = true
	}
	return access
}

func (r *ClientRepo) CheckAccessRecursiveE(userID, targetClientID uuid.UUID) (bool, string, error) {
	return CheckAccessRecursiveE(userID, targetClientID)
}

func (r *ClientRepo) CheckApplicationAccessRecursive(userID, targetClientID uuid.UUID, application string) (ApplicationAccess, error) {
	return CheckApplicationAccessRecursive(userID, targetClientID, application)
}
