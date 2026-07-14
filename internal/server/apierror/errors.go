package apierror

import (
	"errors"
	"fmt"
)

const (
	CodeDeviceNotFound      = "DEVICE_NOT_FOUND"
	CodeDeviceOffline       = "DEVICE_OFFLINE"
	CodeAgentNotFound       = "AGENT_NOT_FOUND"
	CodeAgentUnavailable    = "AGENT_UNAVAILABLE"
	CodeSessionNotFound     = "SESSION_NOT_FOUND"
	CodePairingCodeInvalid  = "PAIRING_CODE_INVALID"
	CodePairingCodeExpired  = "PAIRING_CODE_EXPIRED"
	CodePairingCodeConsumed = "PAIRING_CODE_CONSUMED"
	CodeUnsupportedContent  = "UNSUPPORTED_CONTENT_TYPE"
	CodeUnauthorized        = "UNAUTHORIZED"
	CodeForbidden           = "FORBIDDEN"
	CodeRateLimited         = "RATE_LIMITED"
	CodePayloadTooLarge     = "PAYLOAD_TOO_LARGE"
	CodeInvalidRequest      = "INVALID_REQUEST"
	CodeConflict            = "CONFLICT"
	CodeNotFound            = "NOT_FOUND"
	CodeMethodNotAllowed    = "METHOD_NOT_ALLOWED"
	CodeInternal            = "INTERNAL_ERROR"
)

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"-"`
	Cause   error  `json:"-"`
}

func (e *Error) Error() string {
	if e.Cause == nil {
		return fmt.Sprintf("%s: %s", e.Code, e.Message)
	}
	return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Cause)
}

func (e *Error) Unwrap() error { return e.Cause }

func New(code, message string, status int) *Error {
	return &Error{Code: code, Message: message, Status: status}
}

func Wrap(code, message string, status int, cause error) *Error {
	return &Error{Code: code, Message: message, Status: status, Cause: cause}
}

func As(err error) *Error {
	if err == nil {
		return nil
	}
	var apiErr *Error
	if errors.As(err, &apiErr) {
		return apiErr
	}
	return Wrap(CodeInternal, "Internal server error", 500, err)
}
