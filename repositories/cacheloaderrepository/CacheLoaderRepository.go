package cacheloaderrepository

import (
	"encoding/json"
	"events-stocks/repositories/colorrepository"
	"events-stocks/repositories/eventsrepository"
	"events-stocks/repositories/fontrepository"
	"events-stocks/repositories/resourcerepository"
	"strings"
)

func GetLoader(key string) (func() (string, error), bool) {
	if strings.HasPrefix(key, "all:") {
		resource := strings.TrimPrefix(key, "all:")

		switch resource {
		case "events":
			return ListAllEvents, true
		case "fontsets":
			return ListFontSets, true
		case "colorpalettes":
			return ListColorPalettes, true
		case "resourcetypes":
			return ListResourceTypes, true
		}
	}

	// Static fallback loaders (opcional si usas claves directas sin prefijo)
	loaderMap := map[string]func() (string, error){
		"events":        ListAllEvents,
		"fontsets":      ListFontSets,
		"colorpalettes": ListColorPalettes,
		"resourcetypes": ListResourceTypes,
	}

	loaderFunc, exists := loaderMap[key]
	return loaderFunc, exists
}

func ListAllEvents() (string, error) {
	data, err := eventsrepository.ListEvents(1, 0, "")
	return marshalData(data, err)
}

func ListFontSets() (string, error) {
	data, err := fontrepository.ListFontSets(1, 0, "")
	return marshalData(data, err)
}

func ListColorPalettes() (string, error) {
	data, err := colorrepository.ListColorPalettes()
	return marshalData(data, err)
}

func ListResourceTypes() (string, error) {
	data, err := resourcerepository.ListResourceTypes()
	return marshalData(data, err)
}

func marshalData(data any, err error) (string, error) {
	if err != nil {
		return "", err
	}
	bytes, err := json.Marshal(data)
	if err != nil {
		return "", err
	}
	return string(bytes), nil
}
