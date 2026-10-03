package qaevidence

import "events-stocks/internal/evidencejson"

func validateJSONIntegrity(payload []byte) error {
	return evidencejson.Validate(payload)
}
