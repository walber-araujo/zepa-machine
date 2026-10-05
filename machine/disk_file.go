//go:build unix

package machine

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// AttachDiskFile backs the disk with a file of the given size, so its
// contents survive the simulator. A new or empty file is created sparse with
// that size; an existing file must already have it. The file is mapped
// shared, so every DWRITE reaches it immediately and DREAD/DWRITE are
// unchanged.
func (m *Machine) AttachDiskFile(path string, size int) error {
	if size <= 0 || size%diskBlockSize != 0 {
		return fmt.Errorf("disk size must be a positive multiple of %d bytes, got %d", diskBlockSize, size)
	}

	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}

	info, err := file.Stat()
	if err != nil {
		file.Close()
		return err
	}
	switch {
	case info.Size() == 0:
		err = file.Truncate(int64(size))
	case info.Size() != int64(size):
		err = fmt.Errorf("%s has %d bytes, but the disk size is %d: delete the file or omit disk_size", path, info.Size(), size)
	}
	if err != nil {
		file.Close()
		return err
	}

	data, err := unix.Mmap(int(file.Fd()), 0, size, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	file.Close()
	if err != nil {
		return err
	}

	m.disk = data
	m.diskMapped = true
	return nil
}

// SyncDisk forces the disk file onto the physical disk.
func (m *Machine) SyncDisk() error {
	if !m.diskMapped {
		return nil
	}
	return unix.Msync(m.disk, unix.MS_SYNC)
}
