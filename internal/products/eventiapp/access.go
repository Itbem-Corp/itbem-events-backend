package eventiapp

// OrganizationAdminModules lists the EventiApp modules that only organization
// Owner/Admin members (direct or inherited) and platform roots may use. Other
// organization roles keep their event capabilities but lose these surfaces;
// the backend enforces it through internal/products.ModuleRequiresOrganizationAdmin.
var OrganizationAdminModules = []string{"metrics"}
