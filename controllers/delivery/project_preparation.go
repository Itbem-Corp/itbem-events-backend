package delivery

import (
	"encoding/json"
	"events-stocks/configuration"
	"events-stocks/models"
	"strings"
	"time"
)

// This is a preparation diagnosis, never execution authorization. Runtime,
// budget and permissions are rechecked at dispatch by their existing owners.
type preparationCheck struct {
	Key    string `json:"key"`
	State  string `json:"state"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
}

type projectPreparation struct {
	Version int                `json:"version"`
	Checks  []preparationCheck `json:"checks"`
}

func describeProjectPreparation(project models.DeliveryProject) projectPreparation {
	checks := []preparationCheck{}
	add := func(key, state, title, detail string) {
		checks = append(checks, preparationCheck{key, state, title, detail})
	}
	if strings.TrimSpace(project.Summary) == "" {
		add("objective", "missing", "Objetivo", "Define qué debe conseguir este proyecto.")
	} else {
		add("objective", "ready", "Objetivo", "El proyecto conserva un objetivo compartido.")
	}
	refs := map[string]bool{}
	for _, source := range project.Context {
		if source.Kind == "repository" {
			refs[source.Reference] = true
		}
	}
	local, primary := 0, 0
	invalid := false
	for _, source := range project.Context {
		if source.Kind != "repository" {
			continue
		}
		var metadata map[string]any
		_ = json.Unmarshal([]byte(source.MetadataJSON), &metadata)
		if strings.HasPrefix(source.Reference, "workspace://") {
			local++
			if metadata["repository_role"] == "primary" {
				primary++
			}
			if source.Revision == "" || source.Status != "ready" || metadata["local_workspace_dirty"] == true {
				invalid = true
			}
		}
		if dependencies, ok := metadata["depends_on_repositories"].([]any); ok {
			for _, dependency := range dependencies {
				reference, ok := dependency.(string)
				if !ok || !refs[reference] {
					invalid = true
				}
			}
		}
	}
	if local == 0 {
		add("repositories", "missing", "Código editable", "Conecta un workspace registrado. Una referencia GitHub por sí sola sólo aporta contexto.")
	} else if invalid || (local > 1 && primary != 1) || primary > 1 {
		add("repositories", "missing", "Repositorios", "Revisa revisiones, estado del checkout, repositorio principal y dependencias antes de preparar cambios.")
	} else {
		add("repositories", "ready", "Repositorios", "Las referencias registradas tienen una base para preparar el trabajo; el checkout se verificará de nuevo al ejecutar.")
	}
	add("runtime", "unknown", "Capacidad de ejecución", "La conexión, versión, capacidades y validaciones del worker deben comprobarse al iniciar cada trabajo.")
	add("acceptance", "unknown", "Aceptación del encargo", "Cada trabajo necesita su propio alcance y criterios verificables.")
	add("budget", "unknown", "Presupuesto", "La reserva depende del trabajo y se valida antes de llamar al modelo. Guardar este proyecto no consume inferencia.")
	remotePending, remoteReady := 0, 0
	sandboxUnknown, sandboxMissing, sandboxReady := 0, 0, 0
	for _, source := range project.Context {
		if source.Kind != "repository" {
			continue
		}
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(source.Reference)), "github://") {
			if source.Status == "pending_sync" || strings.TrimSpace(source.Revision) == "" {
				remotePending++
			} else if source.Status == "ready" {
				remoteReady++
			}
		}
		if !strings.HasPrefix(strings.TrimSpace(source.Reference), "workspace://") {
			continue
		}
		var metadata map[string]any
		if strings.TrimSpace(source.MetadataJSON) == "" || json.Unmarshal([]byte(source.MetadataJSON), &metadata) != nil {
			sandboxUnknown++
			continue
		}
		harness, ok := metadata["workspace_harness"].(map[string]any)
		if !ok {
			sandboxUnknown++
			continue
		}
		mode, _ := harness["sandbox_mode"].(string)
		resourcePolicy, _ := harness["sandbox_resource_policy"].(string)
		if strings.EqualFold(strings.TrimSpace(mode), "docker") && strings.TrimSpace(resourcePolicy) != "" {
			sandboxReady++
		} else {
			sandboxMissing++
		}
	}
	if remotePending > 0 {
		add("remote_sync", "missing", "Sincronización remota", "Hay referencias GitHub sin una revisión congelada; actualízalas antes de usarlas como fuente del trabajo.")
	} else if remoteReady > 0 {
		add("remote_sync", "ready", "Sincronización remota", "Las referencias GitHub tienen una revisión congelada para planificación.")
	} else {
		add("remote_sync", "unknown", "Sincronización remota", "No hay una referencia GitHub lista o todavía no se ha verificado.")
	}
	if sandboxMissing > 0 {
		add("sandbox", "missing", "Aislamiento de ejecución", "Uno o más workspaces no tienen un sandbox Docker verificable; no se deben usar para código no confiable.")
	} else if sandboxUnknown > 0 || sandboxReady == 0 {
		add("sandbox", "unknown", "Aislamiento de ejecución", "La disponibilidad del sandbox debe comprobarse en el worker antes de ejecutar comandos.")
	} else {
		add("sandbox", "ready", "Aislamiento de ejecución", "Los workspaces registrados declaran sandbox Docker y límites de recursos.")
	}
	return projectPreparation{Version: 1, Checks: checks}
}

// describeProjectPreparationWithRuntime enriches the durable project
// diagnosis with a short-lived heartbeat signal. It is still advisory: every
// dispatch rechecks capability, workspace, budget and authorization. A live
// worker without the matching workspace sandbox never becomes an execution
// approval merely because its process is alive.
func describeProjectPreparationWithRuntime(project models.DeliveryProject) projectPreparation {
	preparation := describeProjectPreparation(project)
	if configuration.DB == nil {
		return preparation
	}
	workspaceIDs := make([]string, 0)
	for _, source := range project.Context {
		if source.Kind == "repository" && strings.HasPrefix(strings.TrimSpace(source.Reference), "workspace://") {
			workspaceIDs = append(workspaceIDs, strings.TrimPrefix(strings.TrimSpace(source.Reference), "workspace://"))
		}
	}
	var heartbeats []models.AutomationAgentHeartbeat
	if err := configuration.DB.Where("last_seen_at >= ?", time.Now().UTC().Add(-90*time.Second)).Find(&heartbeats).Error; err != nil {
		return preparation
	}
	planningWorkers, implementationWorkers := 0, 0
	implementationWorkspaceReady := len(workspaceIDs) > 0
	workspaceFound := make(map[string]bool, len(workspaceIDs))
	for _, heartbeat := range heartbeats {
		var capabilities []string
		_ = json.Unmarshal([]byte(heartbeat.CapabilitiesJSON), &capabilities)
		if workerSupportsOperation(capabilities, "delivery.plan") {
			planningWorkers++
		}
		if !workerSupportsOperation(capabilities, "delivery.implementation") {
			continue
		}
		var readiness []struct {
			ID            string `json:"id"`
			Ready         bool   `json:"ready"`
			SandboxReady  bool   `json:"sandbox_ready"`
			IsolationMode string `json:"isolation_mode"`
		}
		if json.Unmarshal([]byte(heartbeat.WorkspaceReadiness), &readiness) != nil {
			continue
		}
		for _, workspace := range readiness {
			for _, required := range workspaceIDs {
				if workspace.ID != required {
					continue
				}
				workspaceFound[required] = true
				if implementationWorkspaceSandboxReady(workspace.Ready, workspace.SandboxReady, workspace.IsolationMode) {
					implementationWorkers++
				}
			}
		}
	}
	for _, workspaceID := range workspaceIDs {
		if !workspaceFound[workspaceID] {
			implementationWorkspaceReady = false
		}
	}
	checkState := "unknown"
	detail := "No hay un heartbeat reciente que confirme capacidad de planificación o implementación. Se comprobará de nuevo al iniciar el encargo."
	if planningWorkers > 0 {
		if implementationWorkspaceReady && implementationWorkers > 0 {
			checkState = "ready"
			detail = "Planificación e implementación tienen workers vivos con el workspace y sandbox confirmados; el despacho volverá a comprobar permisos, lease y presupuesto."
		} else if len(workspaceIDs) == 0 {
			detail = "Hay capacidad para planificar, pero todavía no hay un workspace local editable confirmado para implementar."
		} else {
			detail = "Hay capacidad para planificar, pero la implementación aún no tiene todos los workspaces y sandboxes confirmados."
		}
	}
	for index := range preparation.Checks {
		if preparation.Checks[index].Key == "runtime" {
			preparation.Checks[index].State = checkState
			preparation.Checks[index].Detail = detail
			break
		}
	}
	return preparation
}

// implementationWorkspaceSandboxReady is deliberately stricter than generic
// workspace liveness. A host process may be useful for operator-trusted
// diagnostics, but it must never satisfy Delivery implementation readiness for
// code that the agent or model could influence. The worker must attest an
// actual Docker boundary until a VM/microVM runtime is implemented.
func implementationWorkspaceSandboxReady(ready, sandboxReady bool, isolationMode string) bool {
	return ready && sandboxReady && strings.EqualFold(strings.TrimSpace(isolationMode), "docker_container")
}

func workerSupportsOperation(capabilities []string, operation string) bool {
	if len(capabilities) == 0 {
		return true
	}
	for _, capability := range capabilities {
		if strings.EqualFold(strings.TrimSpace(capability), operation) {
			return true
		}
	}
	return false
}
