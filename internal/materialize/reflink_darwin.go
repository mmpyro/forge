//go:build darwin

package materialize

import "golang.org/x/sys/unix"

func reflink(src, dst string) error { return unix.Clonefile(src, dst, unix.CLONE_NOFOLLOW) }
