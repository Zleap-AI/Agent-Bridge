package server

import "github.com/Zleap-AI/Agent-Bridge/internal/server/apierror"

const (
	CodeDeviceNotFound      = apierror.CodeDeviceNotFound
	CodeDeviceOffline       = apierror.CodeDeviceOffline
	CodeAgentNotFound       = apierror.CodeAgentNotFound
	CodeAgentUnavailable    = apierror.CodeAgentUnavailable
	CodeSessionNotFound     = apierror.CodeSessionNotFound
	CodePairingCodeInvalid  = apierror.CodePairingCodeInvalid
	CodePairingCodeExpired  = apierror.CodePairingCodeExpired
	CodePairingCodeConsumed = apierror.CodePairingCodeConsumed
	CodeUnsupportedContent  = apierror.CodeUnsupportedContent
	CodeUnauthorized        = apierror.CodeUnauthorized
	CodeForbidden           = apierror.CodeForbidden
	CodeInvalidRequest      = apierror.CodeInvalidRequest
	CodeConflict            = apierror.CodeConflict
	CodeInternal            = apierror.CodeInternal
)

type Error = apierror.Error

func NewError(code, message string, status int) *Error { return apierror.New(code, message, status) }

func WrapError(code, message string, status int, cause error) *Error {
	return apierror.Wrap(code, message, status, cause)
}

func AsError(err error) *Error { return apierror.As(err) }
