package delivery

import (
	"events-stocks/models"
	"events-stocks/services/deliveryworkflow"
	"sort"
	"strings"
	"time"
)

// deliveryWorkflowProjection is a read-only, server-authoritative view of the
// work item. It is deliberately response-only: workflow mutations still go
// through the existing transition, agent-run, task and message endpoints.
// Keeping this contract next to the control-plane code prevents each client
// from inventing a different interpretation of the same durable state.
type deliveryWorkflowProjection struct {
	SchemaVersion    int                                 `json:"schema_version"`
	Stage            string                              `json:"stage"`
	StateKind        string                              `json:"state_kind"`
	Summary          string                              `json:"summary"`
	Detail           string                              `json:"detail"`
	State            string                              `json:"state"`
	CurrentOperation string                              `json:"current_operation,omitempty"`
	CurrentTaskID    string                              `json:"current_task_id,omitempty"`
	WaitingReason    string                              `json:"waiting_reason,omitempty"`
	WaitingCategory  string                              `json:"waiting_category,omitempty"`
	Actor            deliveryWorkflowProjectionActor     `json:"actor"`
	LastActivityAt   time.Time                           `json:"last_activity_at"`
	StaleAfterSecs   int                                 `json:"stale_after_seconds"`
	Stale            bool                                `json:"stale"`
	Evidence         deliveryWorkflowEvidenceSummary     `json:"evidence"`
	Recovery         *deliveryWorkflowProjectionRecovery `json:"recovery,omitempty"`
	AvailableActions []deliveryWorkflowProjectionAction  `json:"available_actions"`
	CanContinue      bool                                `json:"can_continue"`
}

// deliveryWorkflowProjectionRecovery is guidance, not an authorization token.
// It tells the operator what kind of intervention is safe for the durable
// state; any mutating action must still pass the existing endpoint checks.
type deliveryWorkflowProjectionRecovery struct {
	Mode                string `json:"mode"`
	ReasonCode          string `json:"reason_code,omitempty"`
	Title               string `json:"title"`
	Detail              string `json:"detail"`
	ActionID            string `json:"action_id,omitempty"`
	RequiresHumanReview bool   `json:"requires_human_review"`
}

type deliveryWorkflowProjectionActor struct {
	Type      string `json:"type"`
	Operation string `json:"operation,omitempty"`
	Provider  string `json:"provider,omitempty"`
	Model     string `json:"model,omitempty"`
}

type deliveryWorkflowEvidenceSummary struct {
	Total        int  `json:"total"`
	Validations  int  `json:"validations"`
	HasResult    bool `json:"has_result"`
	HasChanges   bool `json:"has_changes"`
	HasHumanGate bool `json:"has_human_gate"`
}

// deliveryWorkflowProjectionAction is an affordance, not an authorization
// bypass. The endpoint re-checks identity, permissions, state and epoch when
// the user invokes it. The action carries the real operation/transition name
// so a client does not need to infer it from translated copy.
type deliveryWorkflowProjectionAction struct {
	ID                   string `json:"id"`
	Kind                 string `json:"kind"`
	Label                string `json:"label"`
	Permission           string `json:"permission,omitempty"`
	Phase                string `json:"phase,omitempty"`
	Transition           string `json:"transition,omitempty"`
	TaskID               string `json:"task_id,omitempty"`
	RequiresConfirmation bool   `json:"requires_confirmation,omitempty"`
}

func buildDeliveryWorkflowProjection(item models.DeliveryWorkItem, now time.Time) deliveryWorkflowProjection {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC()
	state := strings.TrimSpace(item.State)
	if state == "" {
		state = deliveryworkflow.StatePlanning
	}
	tasks := append([]models.AutomationTask(nil), item.AutomationTasks...)
	sort.SliceStable(tasks, func(i, j int) bool {
		left := tasks[i].UpdatedAt
		if left.IsZero() {
			left = tasks[i].CreatedAt
		}
		right := tasks[j].UpdatedAt
		if right.IsZero() {
			right = tasks[j].CreatedAt
		}
		return left.After(right)
	})

	// delivery.chat is a durable, auditable conversation operation, not a
	// workflow stage. Its retries or malformed provider output must never turn
	// a plan gate into a delivery incident. The chat history still exposes the
	// attempt; this server-authoritative projection only evaluates work that can
	// advance, block, or recover the delivery flow.
	workflowTasks := deliveryProjectionWorkflowTasks(tasks)
	latest := latestDeliveryProjectionTask(workflowTasks)
	active := activeDeliveryProjectionTask(workflowTasks)
	uncertain := latest != nil && latest.Status == "failed" && ambiguousAgentOutcome(latest.ErrorMessage)
	current := active
	if current == nil {
		current = latest
	}

	projection := deliveryWorkflowProjection{
		SchemaVersion:    2,
		State:            state,
		Stage:            deliveryProjectionStage(state),
		StateKind:        deliveryProjectionStateKind(state, active, uncertain),
		LastActivityAt:   deliveryProjectionLastActivity(item, current),
		StaleAfterSecs:   deliveryProjectionStaleAfter(state, active),
		Evidence:         deliveryProjectionEvidence(item),
		AvailableActions: []deliveryWorkflowProjectionAction{},
		CanContinue:      state != deliveryworkflow.StateReleased && state != deliveryworkflow.StateCancelled,
	}
	if current != nil {
		projection.CurrentOperation = strings.TrimSpace(current.Operation)
		projection.CurrentTaskID = current.ID.String()
		projection.Actor = deliveryWorkflowProjectionActor{Type: "agent", Operation: strings.TrimSpace(current.Operation), Provider: strings.TrimSpace(current.Provider), Model: strings.TrimSpace(current.Model)}
	}
	if active != nil {
		projection.WaitingCategory = "execution"
		projection.WaitingReason = "Una operación está en curso; el siguiente resultado aparecerá en la actividad."
		projection.Summary = "Trabajando"
		projection.Detail = "El agente continúa aunque cierres esta página."
		projection.AvailableActions = append(projection.AvailableActions, deliveryWorkflowProjectionAction{ID: "cancel_task", Kind: "task_cancel", Label: "Detener operación", Permission: string(deliveryManage), TaskID: active.ID.String(), RequiresConfirmation: true})
	} else if uncertain {
		projection.StateKind = "uncertain"
		projection.WaitingCategory = "reconciliation"
		projection.WaitingReason = "La operación terminó sin una respuesta concluyente. Confirma el resultado antes de repetirla."
		projection.Summary = "Resultado por confirmar"
		projection.Detail = "Inspecciona la evidencia y reconcilia el efecto antes de iniciar otro intento."
		projection.CanContinue = false
	} else {
		projection.Summary, projection.Detail, projection.WaitingCategory = deliveryProjectionCopy(state, item.AgentProgress, item.BlockedReason)
		projection.Actor = deliveryWorkflowProjectionActor{Type: deliveryProjectionActorType(state)}
		if latest != nil && (latest.Status == "failed" || latest.Status == "dispatch_failed") {
			projection.StateKind = "attention"
			projection.WaitingCategory = "recovery"
			projection.Summary = "Intento por revisar"
			projection.Detail = "El último intento terminó; revisa la causa y la evidencia antes de continuar."
		}
		projection.AvailableActions = append(projection.AvailableActions, deliveryProjectionStateActions(state, latest)...)
	}
	// Review states intentionally wait for a person. Treating that durable gate
	// as missing agent context would send the operator to chat instead of the
	// actual approve/request-changes decision. A real blocked state still wins.
	if item.AgentProgress == "blocked" || state == deliveryworkflow.StateBlocked || (item.AgentProgress == "waiting_for_user" && !deliveryProjectionIsHumanGate(state)) {
		projection.WaitingCategory = "operator_input"
		if strings.TrimSpace(item.BlockedReason) != "" {
			projection.WaitingReason = strings.TrimSpace(item.BlockedReason)
		} else if projection.WaitingReason == "" {
			projection.WaitingReason = "El agente necesita contexto o una decisión para continuar."
		}
		projection.AvailableActions = append(projection.AvailableActions, deliveryWorkflowProjectionAction{ID: "send_context", Kind: "message", Label: "Enviar contexto", Permission: string(deliveryManage), RequiresConfirmation: false})
	}
	projection.Recovery = deliveryProjectionRecovery(item, state, latest, active, uncertain)
	if projection.StaleAfterSecs > 0 && !projection.LastActivityAt.IsZero() {
		projection.Stale = now.Sub(projection.LastActivityAt) > time.Duration(projection.StaleAfterSecs)*time.Second
	}
	if state != deliveryworkflow.StateReleased && state != deliveryworkflow.StateCancelled {
		projection.AvailableActions = append(projection.AvailableActions, deliveryWorkflowProjectionAction{ID: "open_activity", Kind: "navigation", Label: "Ver actividad"})
		projection.AvailableActions = append(projection.AvailableActions, deliveryWorkflowProjectionAction{ID: "open_control", Kind: "navigation", Label: "Abrir controles"})
	}
	return projection
}

func deliveryProjectionRecovery(item models.DeliveryWorkItem, state string, latest, active *models.AutomationTask, uncertain bool) *deliveryWorkflowProjectionRecovery {
	if uncertain {
		return &deliveryWorkflowProjectionRecovery{Mode: "reconcile", Title: "Confirma el resultado antes de repetir", Detail: "La operación terminó sin una respuesta concluyente. Revisa actividad, evidencia y efectos externos; no se ofrece reintento automático.", ActionID: "open_evidence", RequiresHumanReview: true}
	}
	if active != nil {
		if item.AgentProgress == "blocked" || state == deliveryworkflow.StateBlocked {
			return &deliveryWorkflowProjectionRecovery{Mode: "operator_input", Title: "Resuelve el bloqueo registrado", Detail: strings.TrimSpace(item.BlockedReason), ActionID: "send_context", RequiresHumanReview: true}
		}
		return nil
	}
	if deliveryProjectionIsHumanGate(state) && (latest == nil || (latest.Status != "failed" && latest.Status != "dispatch_failed")) {
		return &deliveryWorkflowProjectionRecovery{Mode: "human_review", Title: "Revisa y decide el gate actual", Detail: "La propuesta está detenida en una decisión explícita. Puedes aprobarla o solicitar cambios; el agente no continuará hasta registrar esa decisión.", ActionID: "open_control", RequiresHumanReview: true}
	}
	if item.AgentProgress == "blocked" || state == deliveryworkflow.StateBlocked || (item.AgentProgress == "waiting_for_user" && !deliveryProjectionIsHumanGate(state)) {
		detail := strings.TrimSpace(item.BlockedReason)
		if detail == "" {
			detail = "El agente necesita contexto o una decisión para continuar."
		}
		return &deliveryWorkflowProjectionRecovery{Mode: "operator_input", Title: "Envía el contexto que falta", Detail: detail, ActionID: "send_context", RequiresHumanReview: true}
	}
	if item.AgentProgress == "queued" {
		return &deliveryWorkflowProjectionRecovery{Mode: "resume", Title: "El trabajo está guardado y continuará", Detail: "La continuación ya está persistida y espera capacidad compatible; no necesitas duplicar el encargo.", ActionID: "open_activity", RequiresHumanReview: false}
	}
	if item.AgentProgress == "waiting_for_preview" || state == deliveryworkflow.StatePreviewPending {
		return &deliveryWorkflowProjectionRecovery{Mode: "resume", Title: "Prepara el preview antes de continuar", Detail: "La siguiente fase sólo puede comenzar cuando exista un preview verificable y el gate correspondiente.", ActionID: "open_control", RequiresHumanReview: true}
	}
	if latest == nil || (latest.Status != "failed" && latest.Status != "dispatch_failed") {
		return nil
	}
	lower := strings.ToLower(strings.TrimSpace(latest.ErrorMessage))
	switch {
	case strings.Contains(lower, "budget"), strings.Contains(lower, "presupuesto"):
		return &deliveryWorkflowProjectionRecovery{Mode: "budget", Title: "Revisa la reserva de presupuesto", Detail: "El intento no puede continuar hasta que exista una reserva válida o un límite autorizado.", ActionID: "open_costs", RequiresHumanReview: true}
	case strings.Contains(lower, "credential"), strings.Contains(lower, "credencial"), strings.Contains(lower, "unauthorized"), strings.Contains(lower, "forbidden"):
		return &deliveryWorkflowProjectionRecovery{Mode: "credentials", Title: "Resuelve las credenciales del entorno", Detail: "La ejecución no debe repetirse hasta que el acceso requerido esté configurado y vuelva a validarse.", ActionID: "open_control", RequiresHumanReview: true}
	case latest.Operation == "code.review" && (strings.Contains(lower, "code review") || strings.Contains(lower, "source location") || strings.Contains(lower, "verdict") || strings.Contains(lower, "json")):
		return &deliveryWorkflowProjectionRecovery{Mode: "repair", ReasonCode: "review_contract_invalid", Title: "La revisión devolvió un formato inválido", Detail: "El intento no produjo una respuesta verificable. Ningún gate avanzó; revisa el diagnóstico antes de reintentar sobre el mismo diff congelado.", ActionID: "open_activity", RequiresHumanReview: true}
	case strings.Contains(lower, "validation"), strings.Contains(lower, "test"), strings.Contains(lower, "check"):
		return &deliveryWorkflowProjectionRecovery{Mode: "repair", Title: "Revisa el fallo y repara dentro del alcance", Detail: "El último intento dejó una validación fallida. Revisa el resultado antes de reintentar para no repetir el mismo error.", ActionID: "open_activity", RequiresHumanReview: true}
	default:
		phase := map[string]string{"delivery.plan": "plan", "delivery.implementation": "implementation", "delivery.qa": "qa", "delivery.summary": "summary"}[latest.Operation]
		if phase == "" {
			return nil
		}
		return &deliveryWorkflowProjectionRecovery{Mode: "retry", Title: "Reintenta sólo después de revisar la causa", Detail: "El último intento falló sin efecto externo ambiguo. Puedes revisar la actividad y repetir la fase de forma idempotente.", ActionID: "retry_" + phase, RequiresHumanReview: false}
	}
}

func deliveryProjectionIsHumanGate(state string) bool {
	switch state {
	case deliveryworkflow.StatePlanReview, deliveryworkflow.StateCodeReview, deliveryworkflow.StateQAReview, deliveryworkflow.StateReleaseReview:
		return true
	default:
		return false
	}
}

func latestDeliveryProjectionTask(tasks []models.AutomationTask) *models.AutomationTask {
	if len(tasks) == 0 {
		return nil
	}
	return &tasks[0]
}

func deliveryProjectionWorkflowTasks(tasks []models.AutomationTask) []models.AutomationTask {
	workflow := make([]models.AutomationTask, 0, len(tasks))
	for _, task := range tasks {
		if strings.TrimSpace(task.Operation) == "delivery.chat" {
			continue
		}
		workflow = append(workflow, task)
	}
	return workflow
}

func activeDeliveryProjectionTask(tasks []models.AutomationTask) *models.AutomationTask {
	for index := range tasks {
		if tasks[index].Status == "queued" || tasks[index].Status == "running" || tasks[index].Status == "cancel_requested" {
			return &tasks[index]
		}
	}
	return nil
}

func deliveryProjectionStage(state string) string {
	switch state {
	case deliveryworkflow.StatePlanning, deliveryworkflow.StatePlanReview:
		return "plan"
	case deliveryworkflow.StateImplementation, deliveryworkflow.StateCodeReview:
		return "build"
	case deliveryworkflow.StatePreviewPending:
		return "preview"
	case deliveryworkflow.StateQARunning, deliveryworkflow.StateQAReview:
		return "qa"
	case deliveryworkflow.StateReleaseReview, deliveryworkflow.StateReleased:
		return "release"
	default:
		return "attention"
	}
}

func deliveryProjectionStateKind(state string, active *models.AutomationTask, uncertain bool) string {
	if uncertain {
		return "uncertain"
	}
	if active != nil {
		return "active"
	}
	if state == deliveryworkflow.StateReleased || state == deliveryworkflow.StateCancelled {
		return "terminal"
	}
	if state == deliveryworkflow.StateBlocked {
		return "blocked"
	}
	if strings.HasSuffix(state, "review") || state == deliveryworkflow.StatePlanReview || state == deliveryworkflow.StateCodeReview || state == deliveryworkflow.StateQAReview {
		return "review"
	}
	if state == deliveryworkflow.StatePreviewPending || state == deliveryworkflow.StatePlanning || state == deliveryworkflow.StateImplementation || state == deliveryworkflow.StateQARunning {
		return "waiting"
	}
	return "attention"
}

func deliveryProjectionActorType(state string) string {
	switch state {
	case deliveryworkflow.StatePlanReview, deliveryworkflow.StateCodeReview, deliveryworkflow.StateQAReview, deliveryworkflow.StateReleaseReview:
		return "human"
	case deliveryworkflow.StatePreviewPending:
		return "system"
	default:
		return "agent"
	}
}

func deliveryProjectionLastActivity(item models.DeliveryWorkItem, task *models.AutomationTask) time.Time {
	last := item.UpdatedAt
	if task != nil && task.UpdatedAt.After(last) {
		last = task.UpdatedAt
	}
	if last.IsZero() {
		last = item.CreatedAt
	}
	return last.UTC()
}

func deliveryProjectionStaleAfter(state string, active *models.AutomationTask) int {
	if active != nil {
		return 120
	}
	if state == deliveryworkflow.StateReleased || state == deliveryworkflow.StateCancelled {
		return 0
	}
	if strings.HasSuffix(state, "review") || state == deliveryworkflow.StatePlanReview || state == deliveryworkflow.StateCodeReview || state == deliveryworkflow.StateQAReview {
		return 3600
	}
	return 900
}

func deliveryProjectionCopy(state, progress, blockedReason string) (string, string, string) {
	if strings.TrimSpace(blockedReason) != "" {
		return "Necesita atención", strings.TrimSpace(blockedReason), "blocked"
	}
	switch state {
	case deliveryworkflow.StatePlanning:
		if progress == "queued" {
			return "Siguiente paso en cola", "La continuación está guardada y espera capacidad disponible.", "capacity"
		}
		return "Preparando el plan", "El agente debe convertir el encargo en un plan verificable.", "execution"
	case deliveryworkflow.StatePlanReview:
		return "Plan por revisar", "Revisa alcance y validaciones antes de permitir la implementación.", "human_review"
	case deliveryworkflow.StateImplementation:
		return "Construyendo el cambio", "La implementación debe conservar el alcance aprobado y dejar evidencia.", "execution"
	case deliveryworkflow.StateCodeReview:
		return "Cambio por revisar", "Las pruebas no sustituyen la revisión humana del cambio.", "human_review"
	case deliveryworkflow.StatePreviewPending:
		return "Esperando preview", "Falta un preview verificable antes de iniciar QA.", "preview"
	case deliveryworkflow.StateQARunning:
		return "Verificando el resultado", "QA contrasta el resultado con los criterios de aceptación.", "execution"
	case deliveryworkflow.StateQAReview:
		return "Validación por revisar", "Contrasta lo esperado con las pruebas y la evidencia de uso.", "human_review"
	case deliveryworkflow.StateReleaseReview:
		return "Entrega por autorizar", "La autorización final es independiente de la revisión de código.", "human_review"
	case deliveryworkflow.StateReleased:
		return "Entregado", "La entrega y su evidencia permanecen disponibles.", "terminal"
	case deliveryworkflow.StateCancelled:
		return "Cancelado", "El historial se conserva y los efectos externos no se deshacen automáticamente.", "terminal"
	case deliveryworkflow.StateBlocked:
		return "Necesita atención", "El trabajo está bloqueado hasta resolver la causa registrada.", "operator_input"
	default:
		return "Estado por verificar", "Revisa la actividad y la última evidencia disponible.", "attention"
	}
}

func deliveryProjectionStateActions(state string, latest *models.AutomationTask) []deliveryWorkflowProjectionAction {
	actions := []deliveryWorkflowProjectionAction{}
	if latest != nil && (latest.Status == "failed" || latest.Status == "dispatch_failed") && !ambiguousAgentOutcome(latest.ErrorMessage) {
		phase := map[string]string{"delivery.plan": "plan", "delivery.implementation": "implementation", "delivery.qa": "qa", "delivery.summary": "summary"}[latest.Operation]
		if phase != "" {
			actions = append(actions, deliveryWorkflowProjectionAction{ID: "retry_" + phase, Kind: "agent_run", Label: "Reintentar " + phase, Permission: string(deliveryManage), Phase: phase, RequiresConfirmation: false})
		}
	}
	switch state {
	case deliveryworkflow.StatePlanning:
		actions = append(actions, deliveryWorkflowProjectionAction{ID: "start_plan", Kind: "agent_run", Label: "Iniciar planificación", Permission: string(deliveryManage), Phase: "plan"})
	case deliveryworkflow.StateImplementation:
		actions = append(actions, deliveryWorkflowProjectionAction{ID: "start_implementation", Kind: "agent_run", Label: "Continuar implementación", Permission: string(deliveryManage), Phase: "implementation"})
	case deliveryworkflow.StateQARunning:
		actions = append(actions, deliveryWorkflowProjectionAction{ID: "start_qa", Kind: "agent_run", Label: "Ejecutar QA", Permission: string(deliveryManage), Phase: "qa"})
	case deliveryworkflow.StateReleaseReview:
		actions = append(actions, deliveryWorkflowProjectionAction{ID: "start_summary", Kind: "agent_run", Label: "Preparar resumen", Permission: string(deliveryManage), Phase: "summary"})
	case deliveryworkflow.StatePlanReview:
		actions = append(actions,
			deliveryWorkflowProjectionAction{ID: "approve_plan", Kind: "transition", Label: "Aprobar plan", Permission: string(deliveryReview), Transition: string(deliveryworkflow.ActionApprovePlan), RequiresConfirmation: true},
			deliveryWorkflowProjectionAction{ID: "request_plan_changes", Kind: "transition", Label: "Solicitar cambios al plan", Permission: string(deliveryReview), Transition: string(deliveryworkflow.ActionRequestPlanChanges), RequiresConfirmation: true})
	case deliveryworkflow.StateCodeReview:
		actions = append(actions,
			deliveryWorkflowProjectionAction{ID: "approve_code_review", Kind: "transition", Label: "Aprobar cambio", Permission: string(deliveryReview), Transition: string(deliveryworkflow.ActionApproveCodeReview), RequiresConfirmation: true},
			deliveryWorkflowProjectionAction{ID: "request_code_changes", Kind: "transition", Label: "Solicitar cambios", Permission: string(deliveryReview), Transition: string(deliveryworkflow.ActionRequestCodeChanges), RequiresConfirmation: true})
	case deliveryworkflow.StateQAReview:
		actions = append(actions,
			deliveryWorkflowProjectionAction{ID: "approve_qa", Kind: "transition", Label: "Aprobar QA", Permission: string(deliveryQA), Transition: string(deliveryworkflow.ActionApproveQA), RequiresConfirmation: true},
			deliveryWorkflowProjectionAction{ID: "request_qa_changes", Kind: "transition", Label: "Solicitar correcciones", Permission: string(deliveryQA), Transition: string(deliveryworkflow.ActionRequestQAChanges), RequiresConfirmation: true})
	}
	return actions
}

func deliveryProjectionEvidence(item models.DeliveryWorkItem) deliveryWorkflowEvidenceSummary {
	summary := deliveryWorkflowEvidenceSummary{Total: len(item.Evidence), HasChanges: len(item.ChangeSets) > 0}
	for _, evidence := range item.Evidence {
		if evidence.Kind == "test_result" || evidence.Kind == "report" {
			summary.Validations++
		}
		if evidence.Reference != "" {
			summary.HasResult = true
		}
	}
	for _, gate := range item.Gates {
		if gate.Decision != "" {
			summary.HasHumanGate = true
			break
		}
	}
	return summary
}
