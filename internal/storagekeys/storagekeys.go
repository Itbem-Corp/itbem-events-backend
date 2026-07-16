package storagekeys

import "strings"

// Namespace returns the optional prefix before a canonical media root. Legacy
// keys such as moments/... return an empty namespace; organization-scoped keys
// return organizations/{id}/.
func Namespace(key string) string {
	key = strings.TrimLeft(strings.TrimSpace(key), "/")
	index := -1
	for _, marker := range []string{"moments/", "events/", "resources/"} {
		if candidate := strings.Index(key, marker); candidate >= 0 && (index < 0 || candidate < index) {
			index = candidate
		}
	}
	if index <= 0 {
		return ""
	}
	return key[:index]
}

func Scoped(namespace, key string) string {
	namespace = strings.Trim(strings.TrimSpace(namespace), "/")
	key = strings.TrimLeft(strings.TrimSpace(key), "/")
	if namespace == "" {
		return key
	}
	return namespace + "/" + key
}
