// +build !windows

package main

func (w *fileRotator) truncate() error {
	return w.file.Truncate(0)
}
