//go:build kernel

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseDiskSize(t *testing.T) {
	valid := []struct {
		input    string
		expected int
	}{
		{"4096", 4096},
		{"4096B", 4096},
		{"4KB", 4096},
		{"8kb", 8192},
		{"1MB", 1 << 20},
		{"64mb", 64 << 20},
		{" 8MB ", 8 << 20},
		{"1GB", 1 << 30},
		{"2gb", 2 << 30},
		{"4GB", 1 << 32},
		{"4294967296", 1 << 32},
		{"8GB", 8 << 30},
		{"16GB", 1 << 34},
		{"17179869184", 1 << 34},
	}
	for _, tt := range valid {
		t.Run(tt.input, func(t *testing.T) {
			got, err := parseDiskSize(tt.input)
			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}
			if got != tt.expected {
				t.Errorf("Expected %d, got %d", tt.expected, got)
			}
		})
	}

	invalid := []string{
		"", "abc", "KB", "4KB4", "1.5MB", "0", "0KB", "-4KB", "-1",
		"2KB", "4095", "4097", "5000", "1MB1B",
		"16GB1B", "17GB", "32GB", "17179869185", "17179873280",
		"99999999999GB", "9223372036854775807KB",
	}
	for _, input := range invalid {
		t.Run("invalid "+input, func(t *testing.T) {
			if got, err := parseDiskSize(input); err == nil {
				t.Errorf("Expected an error for %q, got %d", input, got)
			}
		})
	}
}

func diskPath(t *testing.T) string {
	return filepath.Join(t.TempDir(), "sackos.img")
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Expected %s to exist: %v", path, err)
	}
	return info.Size()
}

func TestNewMachineCreatesDiskFile(t *testing.T) {
	const memory = 64 * 1024

	tests := []struct {
		name         string
		diskArg      string
		expectedDisk int
	}{
		{"no argument: disk twice as big as memory", "", 2 * memory},
		{"explicit disk smaller than memory", "4KB", 4096},
		{"explicit disk equal to memory", "64KB", memory},
		{"explicit disk bigger than memory", "1MB", 1 << 20},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := diskPath(t)
			m, err := newMachine(memory, tt.diskArg, path)
			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}
			if len(m.GetDisk()) != tt.expectedDisk {
				t.Errorf("Expected disk of %d bytes, got %d", tt.expectedDisk, len(m.GetDisk()))
			}
			if got := fileSize(t, path); got != int64(tt.expectedDisk) {
				t.Errorf("Expected %s of %d bytes, got %d", path, tt.expectedDisk, got)
			}
			if len(m.GetMemory()) != memory {
				t.Errorf("Expected memory of %d bytes, got %d", memory, len(m.GetMemory()))
			}
		})
	}
}

func TestNewMachineKeepsExistingDisk(t *testing.T) {
	const memory = 64 * 1024
	path := diskPath(t)

	first, err := newMachine(memory, "1MB", path)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	copy(first.GetDisk()[5*4096:], "hello disk")

	for _, diskArg := range []string{"", "1MB", "1024KB", "1048576"} {
		m, err := newMachine(memory, diskArg, path)
		if err != nil {
			t.Fatalf("disk_size %q: unexpected error: %v", diskArg, err)
		}
		if len(m.GetDisk()) != 1<<20 {
			t.Errorf("disk_size %q: expected the existing 1MB disk, got %d bytes", diskArg, len(m.GetDisk()))
		}
		if string(m.GetDisk()[5*4096:5*4096+10]) != "hello disk" {
			t.Errorf("disk_size %q: expected the earlier content to be there", diskArg)
		}
	}
}

func TestNewMachineRejectsMismatchedOrInvalidDisk(t *testing.T) {
	const memory = 64 * 1024

	t.Run("explicit size different from the existing file", func(t *testing.T) {
		path := diskPath(t)
		if _, err := newMachine(memory, "1MB", path); err != nil {
			t.Fatal(err)
		}
		for _, diskArg := range []string{"2MB", "512KB", "4KB"} {
			if _, err := newMachine(memory, diskArg, path); err == nil {
				t.Errorf("disk_size %q: expected an error for a 1MB file", diskArg)
			}
			if got := fileSize(t, path); got != 1<<20 {
				t.Errorf("disk_size %q: the file was resized to %d", diskArg, got)
			}
		}
	})

	t.Run("existing file that is not a whole number of blocks", func(t *testing.T) {
		path := diskPath(t)
		if err := os.WriteFile(path, make([]byte, 5000), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := newMachine(memory, "", path); err == nil {
			t.Errorf("Expected an error for a 5000-byte file")
		}
		if got := fileSize(t, path); got != 5000 {
			t.Errorf("The file was resized to %d", got)
		}
	})

	t.Run("invalid disk_size does not create the file", func(t *testing.T) {
		for _, diskArg := range []string{"abc", "0", "4097", "17GB"} {
			path := diskPath(t)
			if _, err := newMachine(memory, diskArg, path); err == nil {
				t.Errorf("Expected an error for disk_size %q", diskArg)
			}
			if _, err := os.Stat(path); err == nil {
				t.Errorf("disk_size %q: the file was created", diskArg)
			}
		}
	})

	t.Run("unusable path", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "no", "such", "sackos.img")
		if _, err := newMachine(memory, "", path); err == nil {
			t.Errorf("Expected an error for a missing directory")
		}
	})
}
