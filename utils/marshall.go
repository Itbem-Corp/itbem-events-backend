package utils

import "encoding/json"

func MarshallData(data any, err error) (string, error) {
	if err != nil {
		return "", err
	}
	bytes, err := json.Marshal(data)
	if err != nil {
		return "", err
	}
	return string(bytes), nil
}
