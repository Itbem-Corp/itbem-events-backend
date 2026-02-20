package dtos

// AuthUser (Respuesta)
type AuthUser struct {
	Sub       string
	Email     string
	FirstName string
	LastName  string
	IsActive  bool
}

// CreateAuthUserRequest (Petición) 👈 Necesitas esto
type CreateAuthUserRequest struct {
	Email     string
	Password  string
	FirstName string
	LastName  string
}
