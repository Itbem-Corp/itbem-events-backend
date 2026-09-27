package automationagent

// PublicationEffectError preserves a durable partial result when a remote
// publication may have succeeded but the response was lost. Callers must
// reconcile the branch/PR before retrying instead of repeating the effect.
type PublicationEffectError struct {
	Partial map[string]any
	Cause   error
}

func (e *PublicationEffectError) Error() string {
	if e == nil || e.Cause == nil {
		return "publication effect outcome is uncertain; reconciliation required"
	}
	return "publication effect outcome is uncertain; reconciliation required: " + e.Cause.Error()
}

func (e *PublicationEffectError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}
