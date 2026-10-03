package delivery

import (
	"os"
	"time"

	"events-stocks/configuration"
	"events-stocks/internal/automationagent"
	"events-stocks/internal/deliverypolicy"
	"events-stocks/internal/projectvault"
	"events-stocks/models"
	"github.com/labstack/echo/v4"
)

// Readiness is a diagnosis, not a grant. Unknown live prerequisites explicitly
// prevent the platform from advertising autonomous production readiness.
type autonomyReadiness struct {
	Version      int                `json:"version"`
	State        string             `json:"state"`
	CheckedAt    time.Time          `json:"checked_at"`
	PolicyDigest string             `json:"policy_digest,omitempty"`
	Checks       []preparationCheck `json:"checks"`
}

func describeAutonomyReadiness(policy deliverypolicy.ResolvedPolicy, sourceReady, publicationReady bool, now time.Time) autonomyReadiness {
	result := autonomyReadiness{Version: 1, State: "partially_ready", CheckedAt: now.UTC(), PolicyDigest: policy.Digest, Checks: []preparationCheck{}}
	add := func(key, state, title, detail string) {
		result.Checks = append(result.Checks, preparationCheck{key, state, title, detail})
		if state == "missing" {
			result.State = "blocked"
		}
	}
	if !policy.Resolved {
		add("policy", "missing", "Política aprobada", "Completa y aprueba la política efectiva del repositorio antes de activar autonomía.")
	} else if policy.GateApprovalMode != deliverypolicy.GateApprovalDelegated {
		add("policy", "missing", "Autorización delegada", "La política vigente conserva gates humanos por trabajo; una autorización en el chat no modifica esta política.")
	} else {
		add("policy", "ready", "Autorización delegada", "La política aprobada permite delegación; cada trabajo debe congelar su propia autoridad y evidencia.")
	}
	if sourceReady {
		add("source_app", "ready", "Adquisición de fuentes", "La configuración local de Source App es válida; falta comprobar acceso real al repositorio.")
	} else {
		add("source_app", "missing", "Adquisición de fuentes", "Configura la Source App de lectura en el control plane; no se usarán credenciales del operador como alternativa.")
	}
	if policy.Mode == deliverypolicy.ModeMerge || policy.Mode == deliverypolicy.ModeRelease {
		if publicationReady {
			add("publication_app", "ready", "Publicación", "La configuración local de publicación es válida; los grants y permisos se verifican al ejecutar.")
		} else {
			add("publication_app", "missing", "Publicación", "Configura la App de publicación antes de aceptar un recorrido que necesita crear ramas o PRs.")
		}
	}
	add("github_protections", "unknown", "Protecciones de GitHub", "Verifica con las identidades configuradas la aprobación independiente y Bema Review / exact-sha sobre el commit final.")
	add("delegated_coordinator", "missing", "Continuidad delegada", "QA dispone de decisiones delegadas con evidencia sellada y correcciones acotadas. Falta completar las decisiones de plan y código, publicación y release para acreditar autonomía de producción.")
	if policy.Mode == deliverypolicy.ModeRelease {
		add("release_environment", "unknown", "Entorno de release", "Comprueba la promoción autorizada, SHA desplegado, salud y recuperación con el entorno real.")
	}
	return result
}

func GetAutonomyReadiness(c echo.Context) error {
	projectID, err := id(c, "project")
	if err != nil {
		return err
	}
	if _, err := projectActor(c, projectID, deliveryView); err != nil {
		return err
	}
	repository, err := projectvault.CanonicalGitHubReference(c.QueryParam("repository"))
	if err != nil {
		return badRequest(c, "Invalid readiness context", "repository must identify a registered GitHub repository")
	}
	var project models.DeliveryProject
	if err := configuration.DB.First(&project, projectID).Error; err != nil {
		return lookup(c, "Delivery project", err)
	}
	var vault models.DeliveryProjectVaultRevision
	if err := configuration.DB.Where("project_id = ? AND repository_reference = ?", projectID, repository).Order("version DESC").First(&vault).Error; err != nil {
		return lookup(c, "Project Vault repository", err)
	}
	if _, err := frozenWorkItemVault(repository, vault.Revision, []models.DeliveryProjectVaultRevision{vault}); err != nil {
		return conflict(c, "Vault readiness failed", "The registered repository Vault failed integrity checks")
	}
	now := time.Now().UTC()
	policy, err := resolveEffectiveProjectPolicy(configuration.DB, project, repository, "", false, now)
	if err != nil {
		return conflict(c, "Policy readiness failed", "The policy failed integrity checks; no authority was granted")
	}
	_, sourceErr := automationagent.LoadGitHubSourceAppConfig(os.Getenv)
	result := describeAutonomyReadiness(policy, sourceErr == nil, publicationReadinessForEnvironment(os.Getenv).State == "ready", now)
	return success(c, "Delivery autonomy readiness", result)
}
