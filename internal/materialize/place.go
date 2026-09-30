package materialize

import (
	"fmt"
	"io"
	"os"
)

// Swappable for tests.
var (
	reflinkFn  = reflink
	hardlinkFn = os.Link
)

// place puts a copy of the cached blob src at dst, cheapest method first:
// reflink (copy-on-write clone), hardlink (shares the read-only inode),
// plain copy.
func place(src, dst string) error {
	_ = os.Remove(dst)
	if err := reflinkFn(src, dst); err == nil {
		return os.Chmod(dst, 0o644)
	}
	if err := hardlinkFn(src, dst); err == nil {
		return nil
	}
	if err := copyFile(src, dst); err != nil {
		return fmt.Errorf("place %s: %w", dst, err)
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	return out.Close()
}
