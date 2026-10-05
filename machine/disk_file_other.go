//go:build !unix

package machine

import "errors"

func (m *Machine) AttachDiskFile(path string, size int) error {
	return errors.New("disk files are only supported on unix systems")
}

func (m *Machine) SyncDisk() error {
	return nil
}
