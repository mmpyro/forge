package registry

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"oras.land/oras-go/v2/errdef"
	"oras.land/oras-go/v2/registry/remote/errcode"
)

// NotChartError means a manifest exists but carries no Helm chart layer.
type NotChartError struct {
	Ref        string
	MediaTypes []string
}

func (e *NotChartError) Error() string {
	return fmt.Sprintf("%s is not a Helm chart (layer media types: %s)", e.Ref, strings.Join(e.MediaTypes, ", "))
}

// Stable problem codes; they are part of the documented JSON output.
const (
	CodeUnauthorized          = "unauthorized"
	CodeForbidden             = "forbidden"
	CodeNotFound              = "not_found"
	CodeNotAChart             = "not_a_chart"
	CodeDigestMismatch        = "digest_mismatch"
	CodeNoMatchingVersion     = "no_matching_version"
	CodeUnsupportedRepository = "unsupported_repository"
	CodeTimeout               = "timeout"
	CodeUnknown               = "unknown"
)

// Problem is a classified error: a stable code, the HTTP status when the
// registry sent one, a short message and what to do next.
type Problem struct {
	Code       string
	HTTPStatus int
	Message    string
	Hint       string
}

// Classify turns a registry error into a Problem.
func Classify(err error, host string) Problem {
	var nc *NotChartError
	switch {
	case errors.As(err, &nc):
		return Problem{Code: CodeNotAChart, Message: nc.Error()}
	case errors.Is(err, errdef.ErrNotFound):
		return notFound()
	case errors.Is(err, context.DeadlineExceeded):
		return Problem{Code: CodeTimeout, Message: "timed out"}
	}
	var er *errcode.ErrorResponse
	if errors.As(err, &er) {
		switch er.StatusCode {
		case http.StatusUnauthorized:
			return Problem{Code: CodeUnauthorized, HTTPStatus: er.StatusCode, Message: "401 unauthorized",
				Hint: fmt.Sprintf("run 'helm registry login %s'", host)}
		case http.StatusForbidden:
			return Problem{Code: CodeForbidden, HTTPStatus: er.StatusCode, Message: "403 forbidden",
				Hint: "check your permissions on this repository"}
		case http.StatusNotFound:
			return notFound()
		}
		return Problem{Code: CodeUnknown, HTTPStatus: er.StatusCode, Message: err.Error()}
	}
	return Problem{Code: CodeUnknown, Message: err.Error()}
}

func notFound() Problem {
	return Problem{Code: CodeNotFound, HTTPStatus: http.StatusNotFound, Message: "404 not found",
		Hint: "Fix the version, or run 'forge dep update'"}
}

// Explain turns a registry error into one short line that says what to do next.
func Explain(err error, host string) string {
	p := Classify(err, host)
	if p.Code == CodeUnauthorized || p.Code == CodeForbidden {
		return p.Message + "; " + p.Hint
	}
	return p.Message
}
