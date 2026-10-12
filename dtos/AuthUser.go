package dtos

// AuthUser (Respuesta)
type AuthUser struct {
	Sub       string
	Email     string
	FirstName string
	LastName  string
	IsActive  bool
	// Additive identity details read by AdminGetUser. Subject is the
	// immutable "sub" attribute (Sub may hold the sign-in username), Username
	// is the pool username, Status the Cognito UserStatus
	// (e.g. FORCE_CHANGE_PASSWORD) and EmailVerified the email_verified flag.
	Subject       string
	Username      string
	Status        string
	EmailVerified bool
}

// CreateAuthUserRequest (Petición) 👈 Necesitas esto
type CreateAuthUserRequest struct {
	Email     string
	Password  string
	FirstName string
	LastName  string
}
