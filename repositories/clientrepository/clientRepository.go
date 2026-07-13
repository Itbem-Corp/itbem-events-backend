package clientrepository

import (
	"events-stocks/dtos"
	"events-stocks/models"
	"events-stocks/repositories/gormrepository"
	"github.com/gofrs/uuid"
	"golang.org/x/sync/errgroup"
	"gorm.io/gorm"
	"strings"
)

func ListClientsPaginated(userID *uuid.UUID, query dtos.ClientsListQuery) ([]models.Client, int64, error) {
	baseQuery := func() *gorm.DB {
		base := gormrepository.DB().Model(&models.Client{})
		if userID != nil {
			// Owners and admins inherit visibility down their organization tree. Other
			// roles intentionally remain scoped to their direct organization. DISTINCT
			// ON keeps a direct assignment authoritative when both paths exist.
			base = base.Joins(`JOIN (
				WITH RECURSIVE reachable_clients AS (
					SELECT cm.client_id, cr.code AS role_code, 0 AS depth
					FROM client_members cm
					JOIN client_roles cr ON cr.id = cm.client_role_id
					WHERE cm.user_id = ? AND cm.is_active = true
					UNION ALL
					SELECT child.id, reachable_clients.role_code, reachable_clients.depth + 1
					FROM clients child
					JOIN reachable_clients ON child.parent_id = reachable_clients.client_id
					WHERE child.deleted_at IS NULL
						AND UPPER(reachable_clients.role_code) IN ('OWNER', 'ADMIN')
				)
				SELECT DISTINCT ON (client_id)
					client_id,
					CASE WHEN depth = 0 THEN role_code ELSE 'INHERITED_' || role_code END AS access_role
				FROM reachable_clients
				ORDER BY client_id, depth ASC
			) scoped_access ON scoped_access.client_id = clients.id`, *userID)
		}
		if search := strings.TrimSpace(query.Search); search != "" {
			pattern := "%" + strings.ToLower(search) + "%"
			base = base.Where("LOWER(clients.name) LIKE ? OR LOWER(clients.code) LIKE ?", pattern, pattern)
		}
		return base
	}

	var total int64
	var clients []models.Client
	group := new(errgroup.Group)
	group.Go(func() error {
		return baseQuery().Distinct("clients.id").Count(&total).Error
	})
	group.Go(func() error {
		queryDB := baseQuery()
		if userID != nil {
			queryDB = queryDB.Select("clients.*, scoped_access.access_role AS access_role")
		}
		return queryDB.Distinct().
			Joins("ClientType").Joins("Parent").
			Order("clients.name ASC").
			Limit(query.PageSize).Offset((query.Page - 1) * query.PageSize).
			Find(&clients).Error
	})
	if err := group.Wait(); err != nil {
		return nil, 0, err
	}
	return clients, total, nil
}

func CountUserClientStatuses(userID uuid.UUID) (active, inactive int64, err error) {
	err = gormrepository.DB().Model(&models.Client{}).
		Joins("JOIN client_members cm ON cm.client_id = clients.id AND cm.user_id = ? AND cm.is_active = true", userID).
		Select("COUNT(DISTINCT clients.id) FILTER (WHERE clients.is_active = true) AS active, COUNT(DISTINCT clients.id) FILTER (WHERE clients.is_active = false) AS inactive").
		Row().Scan(&active, &inactive)
	return
}

// ... (CreateClient, GetClientByID, UpdateClient, AddMember se quedan igual) ...

func CreateClient(client *models.Client) error {
	return gormrepository.Insert(client)
}

func GetClientByID(id uuid.UUID) (*models.Client, error) {
	var client models.Client
	// Preload Parent para saber si tiene padre al consultar
	err := gormrepository.DB().
		Preload("Parent").
		Preload("ClientType"). // 👈 Nuevo
		First(&client, "id = ?", id).Error

	return &client, err
}

func UpdateClient(client *models.Client) error {
	return gormrepository.DB().
		Model(client).
		Select("Name", "Logo", "Code", "ClientTypeID", "ParentID").
		Updates(client).Error
}

func AddMember(member *models.ClientMember) error {
	return gormrepository.Insert(member)
}

// GetClientsByUser is retained for legacy consumers. It delegates to the
// scoped paginated projection so Owner/Admin inheritance is identical whether
// the caller uses the legacy or current endpoint.
func GetClientsByUser(userID uuid.UUID) ([]models.Client, error) {
	clients, _, err := ListClientsPaginated(&userID, dtos.ClientsListQuery{Page: 1, PageSize: 1000})
	return clients, err
}

// IsMember (Básico): Verifica solo membresía DIRECTA
func IsMember(userID, clientID uuid.UUID) (bool, string) {
	var member models.ClientMember

	// Hacemos Preload de ClientRole para obtener el Code (ej: "Owner", "Admin")
	err := gormrepository.DB().
		Preload("ClientRole").
		Where("user_id = ? AND client_id = ? AND is_active = ?", userID, clientID, true).
		First(&member).Error

	if err != nil {
		return false, ""
	}

	// Retornamos true y el código (ej: "Owner")
	return true, member.ClientRole.Code
}

// CheckAccessRecursive verifica si el usuario tiene acceso directo o heredado
// sobre targetClientID usando una única CTE recursiva de PostgreSQL.
// Reemplaza la implementación recursiva anterior que generaba hasta 6 queries.
func CheckAccessRecursive(userID, targetClientID uuid.UUID) (bool, string) {
	type result struct {
		RoleCode string
		Level    int
	}
	var row result

	err := gormrepository.DB().Raw(`
		WITH RECURSIVE anc AS (
			SELECT id, parent_id, 0 AS level
			FROM clients
			WHERE id = ? AND deleted_at IS NULL
			UNION ALL
			SELECT c.id, c.parent_id, a.level + 1
			FROM clients c
			INNER JOIN anc a ON c.id = a.parent_id
			WHERE c.deleted_at IS NULL
		)
		SELECT cr.code AS role_code, anc.level
		FROM anc
		JOIN client_members cm ON cm.client_id = anc.id
			AND cm.user_id = ?
			AND cm.is_active = true
		JOIN client_roles cr ON cr.id = cm.client_role_id
		ORDER BY anc.level ASC
		LIMIT 1
	`, targetClientID, userID).Scan(&row).Error

	if err != nil || row.RoleCode == "" {
		return false, ""
	}

	// Membresía directa (nivel 0): retornar rol tal cual
	if row.Level == 0 {
		return true, row.RoleCode
	}

	// Heredado desde un ancestro: solo OWNER y ADMIN propagan acceso
	roleUpper := strings.ToUpper(row.RoleCode)
	if roleUpper == "OWNER" || roleUpper == "ADMIN" {
		return true, "INHERITED_" + roleUpper
	}
	return false, ""
}

// 🔥 NUEVO: GetChildrenClients
// Obtiene los sub-clientes (Hijos) de un cliente dado
func GetChildrenClients(parentID uuid.UUID) ([]models.Client, error) {
	var clients []models.Client
	err := gormrepository.DB().
		Preload("ClientType").
		Preload("Parent").
		Where("parent_id = ?", parentID).
		Find(&clients).Error
	return clients, err
}

// ... (DeleteAllMembers y DeleteClient se quedan igual) ...
func DeleteAllMembers(clientID uuid.UUID) error {
	return gormrepository.DB().
		Unscoped().
		Where("client_id = ?", clientID).
		Delete(&models.ClientMember{}).Error
}

func DeleteClient(id uuid.UUID) error {
	return gormrepository.DB().
		Unscoped().
		Where("id = ?", id).
		Delete(&models.Client{}).Error
}

// RemoveMember elimina a un usuario específico de una empresa
func RemoveMember(clientID, userID uuid.UUID) error {
	return gormrepository.DB().
		Where("client_id = ? AND user_id = ?", clientID, userID).
		Delete(&models.ClientMember{}).Error
}

// UpdateMemberRole actualiza el rol de un miembro
func UpdateMemberRole(clientID, userID, newRoleID uuid.UUID) error {
	return gormrepository.DB().
		Model(&models.ClientMember{}).
		Where("client_id = ? AND user_id = ?", clientID, userID).
		Update("client_role_id", newRoleID).Error
}

// GetMemberRole obtiene el rol COMPLETO (con jerarquía) de un usuario en un cliente
// Lo necesitamos para validar si tengo poder para despedir al otro.
func GetMemberRole(clientID, userID uuid.UUID) (*models.ClientRole, error) {
	var member models.ClientMember
	err := gormrepository.DB().
		Preload("ClientRole").
		Where("client_id = ? AND user_id = ?", clientID, userID).
		First(&member).Error

	if err != nil {
		return nil, err
	}
	return &member.ClientRole, nil
}

// GetMembers obtiene todos los miembros de una empresa con sus datos de Usuario y Rol poblados
func GetMembers(clientID uuid.UUID) ([]models.ClientMember, error) {
	var members []models.ClientMember

	err := gormrepository.DB().
		Preload("User").       // Carga los datos de la tabla Users (FirstName, Email, etc.)
		Preload("ClientRole"). // Carga los datos de la tabla ClientRoles (Name, Hierarchy)
		Where("client_id = ? AND is_active = ?", clientID, true).
		Find(&members).Error

	return members, err
}

func ListMembersPage(clientID uuid.UUID, page, pageSize int, search string) ([]models.ClientMember, int64, error) {
	baseQuery := func() *gorm.DB {
		query := gormrepository.DB().Model(&models.ClientMember{}).
			Joins("User").
			Where("client_members.client_id = ? AND client_members.is_active = ?", clientID, true)
		if normalized := strings.TrimSpace(search); normalized != "" {
			like := "%" + strings.ToLower(normalized) + "%"
			query = query.Where("LOWER(\"User\".first_name) LIKE ? OR LOWER(\"User\".last_name) LIKE ? OR LOWER(\"User\".email) LIKE ?", like, like, like)
		}
		return query
	}

	var total int64
	var members []models.ClientMember
	group := new(errgroup.Group)
	group.Go(func() error {
		return baseQuery().Count(&total).Error
	})
	group.Go(func() error {
		return baseQuery().Joins("ClientRole").
			Order("\"User\".first_name ASC, \"User\".last_name ASC").
			Limit(pageSize).Offset((page - 1) * pageSize).Find(&members).Error
	})
	if err := group.Wait(); err != nil {
		return nil, 0, err
	}
	return members, total, nil
}

func ListClientsByUser(userID uuid.UUID) ([]models.Client, error) {
	var clients []models.Client

	err := gormrepository.DB().
		Preload("ClientType").
		Preload("Parent").
		Joins("JOIN client_members ON client_members.client_id = clients.id").
		Where("client_members.user_id = ? AND client_members.is_active = true", userID).
		Where("clients.deleted_at IS NULL").
		Find(&clients).
		Error

	return clients, err
}

// CountClientsByUsers returns client counts for multiple users in a single query.
func CountClientsByUsers(userIDs []uuid.UUID) (map[uuid.UUID]int64, error) {
	if len(userIDs) == 0 {
		return map[uuid.UUID]int64{}, nil
	}
	type row struct {
		UserID uuid.UUID
		Count  int64
	}
	var rows []row
	err := gormrepository.DB().
		Model(&models.ClientMember{}).
		Select("user_id, COUNT(*) as count").
		Where("user_id IN ? AND is_active = true", userIDs).
		Group("user_id").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	counts := make(map[uuid.UUID]int64, len(rows))
	for _, r := range rows {
		counts[r.UserID] = r.Count
	}
	return counts, nil
}

// ClientRepo implements ports.ClientRepository.
type ClientRepo struct{}

func NewClientRepo() *ClientRepo { return &ClientRepo{} }

func (r *ClientRepo) CreateClient(client *models.Client) error           { return CreateClient(client) }
func (r *ClientRepo) GetClientByID(id uuid.UUID) (*models.Client, error) { return GetClientByID(id) }
func (r *ClientRepo) UpdateClient(client *models.Client) error           { return UpdateClient(client) }
func (r *ClientRepo) DeleteClient(id uuid.UUID) error                    { return DeleteClient(id) }
func (r *ClientRepo) GetAllClients() ([]models.Client, error)            { return GetAllClients() }
func (r *ClientRepo) GetClientsByUser(userID uuid.UUID) ([]models.Client, error) {
	return GetClientsByUser(userID)
}
func (r *ClientRepo) ListClientsPaginated(userID *uuid.UUID, query dtos.ClientsListQuery) ([]models.Client, int64, error) {
	return ListClientsPaginated(userID, query)
}
func (r *ClientRepo) GetChildrenClients(parentID uuid.UUID) ([]models.Client, error) {
	return GetChildrenClients(parentID)
}
func (r *ClientRepo) CheckAccessRecursive(userID, targetClientID uuid.UUID) (bool, string) {
	return CheckAccessRecursive(userID, targetClientID)
}
func (r *ClientRepo) IsMember(userID, clientID uuid.UUID) (bool, string) {
	return IsMember(userID, clientID)
}
func (r *ClientRepo) AddMember(member *models.ClientMember) error { return AddMember(member) }
func (r *ClientRepo) RemoveMember(clientID, userID uuid.UUID) error {
	return RemoveMember(clientID, userID)
}
func (r *ClientRepo) UpdateMemberRole(clientID, userID, newRoleID uuid.UUID) error {
	return UpdateMemberRole(clientID, userID, newRoleID)
}
func (r *ClientRepo) GetMemberRole(clientID, userID uuid.UUID) (*models.ClientRole, error) {
	return GetMemberRole(clientID, userID)
}
func (r *ClientRepo) GetMembers(clientID uuid.UUID) ([]models.ClientMember, error) {
	return GetMembers(clientID)
}
func (r *ClientRepo) ListMembersPage(clientID uuid.UUID, page, pageSize int, search string) ([]models.ClientMember, int64, error) {
	return ListMembersPage(clientID, page, pageSize, search)
}
func (r *ClientRepo) CountUserClientStatuses(userID uuid.UUID) (int64, int64, error) {
	return CountUserClientStatuses(userID)
}
func (r *ClientRepo) DeleteAllMembers(clientID uuid.UUID) error { return DeleteAllMembers(clientID) }
func (r *ClientRepo) ListClientsByUser(userID uuid.UUID) ([]models.Client, error) {
	return ListClientsByUser(userID)
}
func (r *ClientRepo) CountClientsByUsers(userIDs []uuid.UUID) (map[uuid.UUID]int64, error) {
	return CountClientsByUsers(userIDs)
}

// GetAllClients returns every non-deleted client (for root/super-admin use).
func GetAllClients() ([]models.Client, error) {
	var clients []models.Client
	err := gormrepository.DB().
		Preload("ClientType").
		Preload("Parent").
		Order("name ASC").
		Find(&clients).Error
	return clients, err
}
