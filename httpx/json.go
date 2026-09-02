package httpx

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

const DefaultMaxBodyBytes int64 = 1 << 20

func DecodeJSON(w http.ResponseWriter, request *http.Request, target any, maxBytes int64) error {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBodyBytes
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, request.Body, maxBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return errors.New("request contains multiple JSON values")
	} else if !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func WriteJSON(w http.ResponseWriter, status int, payload any) error {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	return json.NewEncoder(w).Encode(payload)
}

func WriteError(w http.ResponseWriter, status int, code string) error {
	return WriteJSON(w, status, map[string]string{"error": code})
}
