//go:build windows

package atomicfile

// The temporary file itself is flushed before rename. Windows does not expose
// the Unix directory-fsync operation through os.File, so there is no additional
// directory handle to flush here.
func syncDir(string) error {
	return nil
}
