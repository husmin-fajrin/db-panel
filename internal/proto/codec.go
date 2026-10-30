package proto

import (
	"encoding/json"
	"fmt"
)

// Encode serialises an envelope to its wire form.
func Encode(e Envelope) ([]byte, error) {
	return json.Marshal(e)
}

// Decode parses a wire frame. It fails if the kind is unknown or the body
// matching the kind is absent.
func Decode(b []byte) (Envelope, error) {
	var e Envelope
	if err := json.Unmarshal(b, &e); err != nil {
		return Envelope{}, fmt.Errorf("proto: decode: %w", err)
	}
	switch e.Kind {
	case KindRequest:
		if e.Request == nil {
			return Envelope{}, fmt.Errorf("proto: request body missing")
		}
	case KindResponse:
		if e.Response == nil {
			return Envelope{}, fmt.Errorf("proto: response body missing")
		}
	case KindEvent:
		if e.Event == nil {
			return Envelope{}, fmt.Errorf("proto: event body missing")
		}
	default:
		return Envelope{}, fmt.Errorf("proto: unknown kind %q", e.Kind)
	}
	return e, nil
}

// NewResponse builds a successful response, marshalling result as JSON. A nil
// result yields an empty Result.
func NewResponse(id string, result any) (Response, error) {
	r := Response{ID: id, OK: true}
	if result == nil {
		return r, nil
	}
	b, err := json.Marshal(result)
	if err != nil {
		return Response{}, fmt.Errorf("proto: marshal result: %w", err)
	}
	r.Result = b
	return r, nil
}

// ErrorResponse builds a failed response.
func ErrorResponse(id, code, msg string) Response {
	return Response{ID: id, OK: false, Error: &Error{Code: code, Message: msg}}
}
