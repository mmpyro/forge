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

// Method is how an archive was placed.
type Method string

const (
	Reflink  Method = "reflink"
	Hardlink Method = "hardlink"
	Copy     Method = "copy"
)

// place puts a copy of the cached blob src at dst, cheapest method first:
// reflink (copy-on-write clone), hardlink (shares the read-only inode),
// plain copy. It reports the method that worked.
func place(src, dst string) (Method, error) {
	_ = os.Remove(dst)
	if err := reflinkFn(src, dst); err == nil {
		return Reflink, os.Chmod(dst, 0o644)
	}
	if err := hardlinkFn(src, dst); err == nil {
		return Hardlink, nil
	}
	if err := copyFile(src, dst); err != nil {
		return "", fmt.Errorf("place %s: %w", dst, err)
	}
	return Copy, nil
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
