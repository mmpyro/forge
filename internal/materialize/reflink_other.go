//go:build !darwin && !linux

package materialize

import "errors"

func reflink(string, string) error { return errors.ErrUnsupported }
