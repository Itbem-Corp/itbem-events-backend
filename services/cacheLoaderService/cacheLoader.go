package cacheLoader

import (
	"encoding/json"
	eventService "events-stocks/services/eventsService"
	"strings"
)

func GetLoader(key string) (func() (string, error), bool) {
	// Dynamic loader for keys like "all:events"
	if strings.HasPrefix(key, "all:") {
		resource := strings.TrimPrefix(key, "all:")

		switch resource {
		case "events":
			return ListAllEvents, true
		}
	}

	// Static fallback loaders (if needed in the future)
	loaderMap := map[string]func() (string, error){
		"events": ListAllEvents, // optional
	}

	loaderFunc, exists := loaderMap[key]
	return loaderFunc, exists
}

func ListAllEvents() (string, error) {
	events, err := eventService.ListEvents(1, 0, "")
	if err != nil {
		return "", err
	}

	data, err := json.Marshal(events)
	if err != nil {
		return "", err
	}

	return string(data), nil
}
