package delivery

import (
	"errors"
	"events-stocks/internal/automationagent"
	"events-stocks/utils"
	"net/http"
	"os"
	"time"

	"github.com/labstack/echo/v4"
)

// publicationReadiness is intentionally credential-free. It tells an
// authorized operator whether this control plane can safely issue a
// publication grant, without ever exposing IDs, endpoints, tokens or key
// material.
type publicationReadiness struct {
	State        string   `json:"state"`
	Provider     string   `json:"provider"`
	Message      string   `json:"message"`
	Requirements []string `json:"requirements,omitempty"`
}

func publicationReadinessForEnvironment(lookup func(string) string) publicationReadiness {
	if _, err := automationagent.LoadGitHubAppConfig(lookup); err != nil {
		if errors.Is(err, automationagent.ErrGitHubAppNotConfigured) {
			return publicationReadiness{
				State:    "not_configured",
				Provider: "github_app",
				Message:  "La integraci\u00f3n GitHub App a\u00fan no est\u00e1 configurada en este ambiente. El trabajo permanece local y no se emitir\u00e1 ning\u00fan permiso de publicaci\u00f3n.",
				Requirements: []string{
					"Instala una GitHub App con acceso s\u00f3lo a los repositorios que Delivery podr\u00e1 publicar.",
					"Concede exclusivamente Contents: lectura y escritura, y Pull requests: lectura y escritura.",
					"Configura la identidad de la App, su instalaci\u00f3n y su llave privada como secretos del control plane; nunca en el dashboard, el prompt ni un repositorio.",
				},
			}
		}
		return publicationReadiness{
			State:    "invalid",
			Provider: "github_app",
			Message:  "La configuraci\u00f3n de GitHub App no es v\u00e1lida en este ambiente. Revisa los secretos del entorno; las credenciales nunca se muestran en la plataforma.",
			Requirements: []string{
				"Verifica la identidad de la App, la instalaci\u00f3n seleccionada y la llave privada en el entorno del control plane.",
				"Comprueba que la App est\u00e9 instalada en el repositorio revisado antes de emitir un permiso nuevo.",
			},
		}
	}
	return publicationReadiness{
		State:    "ready",
		Provider: "github_app",
		Message:  "La integraci\u00f3n GitHub App est\u00e1 preparada para emitir permisos temporales. Cada publicaci\u00f3n seguir\u00e1 requiriendo un gate humano, un worktree revisado y un grant vigente.",
		Requirements: []string{
			"Cada uso crea un token de instalaci\u00f3n ef\u00edmero s\u00f3lo al publicar una rama ya revisada.",
			"El permiso temporal no autoriza merge, release ni cambios de configuraci\u00f3n remota.",
		},
	}
}

// PublicationReadiness returns the local control-plane posture for a project.
// It deliberately does not mint a token nor make a network request; the worker
// mints a short-lived installation token only when an approved grant is used.
func PublicationReadiness(c echo.Context) error {
	projectID, err := id(c, "project")
	if err != nil {
		return err
	}
	if _, err := projectActor(c, projectID, deliveryManage); err != nil {
		return err
	}
	return success(c, "Delivery publication readiness", publicationReadinessForEnvironment(os.Getenv))
}

// VerifyPublicationReadiness is an explicit administrator action. It mints a
// short-lived installation token and performs exactly one read-only GitHub
// verification; neither the token nor repository metadata leaves the request.
func VerifyPublicationReadiness(c echo.Context) error {
	projectID, err := id(c, "project")
	if err != nil {
		return err
	}
	if _, err := projectActor(c, projectID, deliveryManage); err != nil {
		return err
	}
	readiness := publicationReadinessForEnvironment(os.Getenv)
	if readiness.State != "ready" {
		return utils.Error(c, http.StatusConflict, "GitHub App verification unavailable", readiness.Message)
	}
	config, err := automationagent.LoadGitHubAppConfig(os.Getenv)
	if err != nil {
		return utils.Error(c, http.StatusConflict, "GitHub App verification unavailable", "GitHub App configuration changed while verifying")
	}
	checkedAt := time.Now().UTC()
	if err := automationagent.VerifyGitHubAppInstallation(c.Request().Context(), config, nil, checkedAt); err != nil {
		return utils.Error(c, http.StatusServiceUnavailable, "GitHub App verification failed", "The installation could not issue a usable read-only token")
	}
	return success(c, "GitHub App verification succeeded", map[string]any{
		"state":      "verified",
		"checked_at": checkedAt,
		"provider":   "github_app",
	})
}
