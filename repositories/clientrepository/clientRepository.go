package clientrepository

import (
	"events-stocks/models"
	"events-stocks/repositories/gormrepository"
	"github.com/gofrs/uuid"
	"strings"
)

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

// GetClientsByUser (Se queda igual, devuelve membresías directas)
func GetClientsByUser(userID uuid.UUID) ([]models.Client, error) {
	var clients []models.Client
	err := gormrepository.DB().
		Preload("ClientType").
		Table("clients").
		Joins("JOIN client_members ON client_members.client_id = clients.id").
		Where("client_members.user_id = ? AND client_members.is_active = ?", userID, true).
		Find(&clients).Error
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
	err := gormrepository.DB().Where("parent_id = ?", parentID).Find(&clients).Error
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

func ListClientsByUser(userID uuid.UUID) ([]models.Client, error) {
	var clients []models.Client

	err := gormrepository.DB().
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

func (r *ClientRepo) CreateClient(client *models.Client) error   { return CreateClient(client) }
func (r *ClientRepo) GetClientByID(id uuid.UUID) (*models.Client, error) { return GetClientByID(id) }
func (r *ClientRepo) UpdateClient(client *models.Client) error   { return UpdateClient(client) }
func (r *ClientRepo) DeleteClient(id uuid.UUID) error             { return DeleteClient(id) }
func (r *ClientRepo) GetClientsByUser(userID uuid.UUID) ([]models.Client, error) {
	return GetClientsByUser(userID)
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
func (r *ClientRepo) AddMember(member *models.ClientMember) error  { return AddMember(member) }
func (r *ClientRepo) RemoveMember(clientID, userID uuid.UUID) error { return RemoveMember(clientID, userID) }
func (r *ClientRepo) UpdateMemberRole(clientID, userID, newRoleID uuid.UUID) error {
	return UpdateMemberRole(clientID, userID, newRoleID)
}
func (r *ClientRepo) GetMemberRole(clientID, userID uuid.UUID) (*models.ClientRole, error) {
	return GetMemberRole(clientID, userID)
}
func (r *ClientRepo) GetMembers(clientID uuid.UUID) ([]models.ClientMember, error) {
	return GetMembers(clientID)
}
func (r *ClientRepo) DeleteAllMembers(clientID uuid.UUID) error { return DeleteAllMembers(clientID) }
func (r *ClientRepo) ListClientsByUser(userID uuid.UUID) ([]models.Client, error) {
	return ListClientsByUser(userID)
}
func (r *ClientRepo) CountClientsByUsers(userIDs []uuid.UUID) (map[uuid.UUID]int64, error) {
	return CountClientsByUsers(userIDs)
}
