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

// Explain turns a registry error into one short line that says what to do next.
func Explain(err error, host string) string {
	var nc *NotChartError
	switch {
	case errors.As(err, &nc):
		return nc.Error()
	case errors.Is(err, errdef.ErrNotFound):
		return "404 not found"
	case errors.Is(err, context.DeadlineExceeded):
		return "timed out"
	}
	var er *errcode.ErrorResponse
	if errors.As(err, &er) {
		switch er.StatusCode {
		case http.StatusUnauthorized:
			return fmt.Sprintf("401 unauthorized; run 'helm registry login %s'", host)
		case http.StatusForbidden:
			return "403 forbidden; check your permissions on this repository"
		case http.StatusNotFound:
			return "404 not found"
		}
	}
	return err.Error()
}
