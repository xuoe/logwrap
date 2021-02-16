// +build windows

package main

import "os"

func (w *fileRotator) truncate() error {
	// FIXME: For whatever reason, f.Truncate(0) fails on Windows with "Access
	// Denied", even if the file has already been written to. Possibly some
	// locking mechanism?
	return os.Truncate(w.file.Name(), 0)
}
