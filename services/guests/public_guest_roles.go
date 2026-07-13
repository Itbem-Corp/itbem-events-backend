package guests

import "strings"

func IsPublicHostRole(role string) bool {
	switch NormalizePublicGuestRole(role) {
	case "host", "hosts", "cohost", "cohosts", "anfitrion", "anfitriona", "anfitriones":
		return true
	default:
		return false
	}
}

func IsPublicGraduateRole(role string) bool {
	switch NormalizePublicGuestRole(role) {
	case "graduate", "graduates", "graduatestudent", "graduatestudents", "graduado", "graduada", "graduados", "graduadas":
		return true
	default:
		return false
	}
}

func NormalizePublicGuestRole(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	replacer := strings.NewReplacer(
		"\u00e1", "a",
		"\u00e9", "e",
		"\u00ed", "i",
		"\u00f3", "o",
		"\u00fa", "u",
		"\u00fc", "u",
		"\u00f1", "n",
	)
	return stripPublicGuestRoleSeparators(replacer.Replace(normalized))
}

func stripPublicGuestRoleSeparators(value string) string {
	replacer := strings.NewReplacer(" ", "", "\t", "", "-", "", "_", "")
	return replacer.Replace(value)
}
