package registry

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"oras.land/oras-go/v2/errdef"
	"oras.land/oras-go/v2/registry/remote/errcode"
)

func TestClassifyAndExplain(t *testing.T) {
	for _, tc := range []struct {
		err     error
		code    string
		status  int
		explain string
	}{
		{&NotChartError{Ref: "r:1", MediaTypes: []string{"x"}}, CodeNotAChart, 0, "r:1 is not a Helm chart (layer media types: x)"},
		{fmt.Errorf("wrap: %w", errdef.ErrNotFound), CodeNotFound, 404, "404 not found"},
		{context.DeadlineExceeded, CodeTimeout, 0, "timed out"},
		{&errcode.ErrorResponse{StatusCode: 401}, CodeUnauthorized, 401, "401 unauthorized; run 'helm registry login h'"},
		{&errcode.ErrorResponse{StatusCode: 403}, CodeForbidden, 403, "403 forbidden; check your permissions on this repository"},
		{&errcode.ErrorResponse{StatusCode: 404}, CodeNotFound, 404, "404 not found"},
		{errors.New("boom"), CodeUnknown, 0, "boom"},
	} {
		p := Classify(tc.err, "h")
		if p.Code != tc.code || p.HTTPStatus != tc.status {
			t.Errorf("%v: got %s/%d, want %s/%d", tc.err, p.Code, p.HTTPStatus, tc.code, tc.status)
		}
		if got := Explain(tc.err, "h"); got != tc.explain {
			t.Errorf("Explain(%v) = %q, want %q", tc.err, got, tc.explain)
		}
	}
}
