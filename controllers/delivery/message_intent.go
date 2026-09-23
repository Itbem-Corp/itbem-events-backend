package delivery

import (
	"encoding/json"
	"strings"
	"unicode"
)

type deliveryMessageClassification struct {
	Intent            string
	Effect            string
	RequiresHumanGate bool
	Next              string
}

// classifyDeliveryMessage is intentionally deterministic. It is a UX and
// safety signal, not an authorization layer: workflow permissions, state
// transitions and worker capabilities remain authoritative on the server.
func classifyDeliveryMessage(body string, continuationRequested bool) deliveryMessageClassification {
	normalized := normalizeDeliveryMessage(body)
	question := strings.Contains(normalized, "?") || containsAny(normalized,
		"que esta pasando", "que paso", "como va", "por que", "explica", "status", "how ", "what ", "why ", "can you explain")
	readOnlyRequest := containsAny(normalized, "solo dime", "sólo dime", "dime que falta", "dime qué falta", "describe ", "indica ")
	action := containsAny(normalized,
		"publica", "publicar", "deploy", "release", "merge", "borra", "elimina", "crea", "ejecuta", "run ",
		"reintenta", "reintentar", "reanuda", "reanudar", "continua", "continuar", "resume", "implementa", "implementar", "implement", "arregla", "arreglar ", "fix ", "cambia", "cambiar ", "change ", "ejecuta", "ejecutar ", "crea", "crear ")
	// A hypothetical question can contain an action verb without authorizing
	// that action. Keep this branch ahead of the action classifier so prompts
	// such as "¿qué pasa si publicamos?" remain informational and do not look
	// like a stored instruction in the console.
	hypothetical := containsAny(normalized,
		"que pasa si", "qué pasa si", "que ocurre si", "qué ocurre si", "que sucederia si", "qué sucedería si",
		"si publico", "si publicamos", "si hacemos", "deberiamos", "deberíamos", "podemos ",
		// A polite question is still exploratory when it carries a question
		// mark. Without the mark it remains an instruction, so the action
		// branch below can preserve the durable, non-executing receipt.
		"puedes ", "podrias ", "podrías ", "es posible ", "seria posible ", "sería posible ")

	classification := deliveryMessageClassification{Intent: "context", Effect: "stored_context", Next: "No cambia la ejecución actual; queda disponible como contexto versionado."}
	if (question || readOnlyRequest) && !continuationRequested && (!action || hypothetical || readOnlyRequest) {
		classification.Intent = "question"
		classification.Effect = "informational"
		classification.Next = "No cambia el plan ni inicia un intento; requiere una respuesta o interacción posterior."
		return classification
	}
	if action || continuationRequested {
		classification.Intent = "action_request"
		classification.RequiresHumanGate = containsAny(normalized, "publica", "publicar", "deploy", "release", "merge", "borra", "elimina")
		if continuationRequested {
			classification.Effect = "continuation_queued"
			classification.Next = "Se creó un nuevo intento sólo después de validar estado, epoch, presupuesto y permisos."
		} else {
			classification.Effect = "stored_instruction"
			classification.Next = "No se ejecuta automáticamente; se incorporará en el siguiente punto seguro autorizado."
		}
		return classification
	}
	return classification
}

func deliveryMessageReceipt(classification deliveryMessageClassification) string {
	receipt, _ := json.Marshal(map[string]any{
		"version":             1,
		"classification":      classification.Intent,
		"effect":              classification.Effect,
		"requires_human_gate": classification.RequiresHumanGate,
		"next":                classification.Next,
		"status":              "received",
	})
	return string(receipt)
}

func normalizeDeliveryMessage(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return ' '
		}
		return r
	}, value)
	return strings.Join(strings.Fields(value), " ")
}

func containsAny(value string, markers ...string) bool {
	for _, marker := range markers {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
}
