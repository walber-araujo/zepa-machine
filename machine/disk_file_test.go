//go:build unix

package machine

import (
	"bytes"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func newDiskFileMachine(t *testing.T, path string, size int) *Machine {
	t.Helper()
	machine := NewMachineWithDisk(diskTestMemory, 0, false)
	if err := machine.AttachDiskFile(path, size); err != nil {
		t.Fatalf("Error attaching the disk file: %v", err)
	}
	return machine
}

func TestAttachDiskFileCreatesZeroedFile(t *testing.T) {
	for _, blocks := range []int{1, 2, 17, 1024} {
		path := filepath.Join(t.TempDir(), "disk.img")
		machine := newDiskFileMachine(t, path, blocks*diskBlockSize)

		if len(machine.disk) != blocks*diskBlockSize {
			t.Errorf("%d blocks: expected disk of %d bytes, got %d", blocks, blocks*diskBlockSize, len(machine.disk))
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("%d blocks: file not created: %v", blocks, err)
		}
		if info.Size() != int64(blocks*diskBlockSize) {
			t.Errorf("%d blocks: expected file of %d bytes, got %d", blocks, blocks*diskBlockSize, info.Size())
		}
		if !bytes.Equal(machine.disk, make([]byte, len(machine.disk))) {
			t.Errorf("%d blocks: expected a new disk to be zeroed", blocks)
		}
	}
}

func TestAttachDiskFileIsSparse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "disk.img")
	size := 256 << 20
	newDiskFileMachine(t, path, size)

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	used := info.Sys().(*syscall.Stat_t).Blocks * 512
	if used >= int64(size)/2 {
		t.Skipf("filesystem does not make sparse files: %d of %d bytes allocated", used, size)
	}
}

func TestAttachDiskFileExistingContentIsVisible(t *testing.T) {
	path := filepath.Join(t.TempDir(), "disk.img")
	content := make([]byte, 4*diskBlockSize)
	for block := 0; block < 4; block++ {
		fillPattern(content[block*diskBlockSize:(block+1)*diskBlockSize], byte(block*31))
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}

	machine := newDiskFileMachine(t, path, len(content))

	if !bytes.Equal(machine.disk, content) {
		t.Errorf("Expected the disk to hold the existing file content")
	}
	machine.registers[w1] = 2
	machine.registers[w2] = diskBlockSize
	machine.execute(Instruction{opcode: DREAD, rs1: w1, rs2: w2})
	if !bytes.Equal(machine.memory[diskBlockSize:2*diskBlockSize], content[2*diskBlockSize:3*diskBlockSize]) {
		t.Errorf("Expected DREAD to read block 2 of the existing file")
	}
}

func TestDWRITEReachesFileImmediately(t *testing.T) {
	for _, block := range []int{0, 1, 5, 7} {
		path := filepath.Join(t.TempDir(), "disk.img")
		machine := newDiskFileMachine(t, path, 8*diskBlockSize)
		fillPattern(machine.memory[0:diskBlockSize], byte(block)+1)

		machine.registers[w1] = uint32(block)
		machine.registers[w2] = 0
		machine.execute(Instruction{opcode: DWRITE, rs1: w1, rs2: w2})

		onDisk, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(onDisk) != 8*diskBlockSize {
			t.Fatalf("block %d: file changed size to %d", block, len(onDisk))
		}
		if !bytes.Equal(onDisk[block*diskBlockSize:(block+1)*diskBlockSize], machine.memory[0:diskBlockSize]) {
			t.Errorf("block %d: file does not hold the written block", block)
		}
		for i := 0; i < 8; i++ {
			if i != block && !bytes.Equal(onDisk[i*diskBlockSize:(i+1)*diskBlockSize], make([]byte, diskBlockSize)) {
				t.Errorf("block %d: DWRITE changed block %d of the file", block, i)
			}
		}
	}
}

func TestDiskFilePersistsAcrossMachines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "disk.img")
	first := newDiskFileMachine(t, path, 16*diskBlockSize)

	written := map[uint32][]byte{}
	for _, block := range []uint32{0, 1, 9, 15} {
		fillPattern(first.memory[0:diskBlockSize], byte(block)*13+1)
		written[block] = bytes.Clone(first.memory[0:diskBlockSize])
		first.registers[w1] = block
		first.registers[w2] = 0
		first.execute(Instruction{opcode: DWRITE, rs1: w1, rs2: w2})
	}
	if err := first.SyncDisk(); err != nil {
		t.Fatalf("Error syncing the disk: %v", err)
	}

	second := newDiskFileMachine(t, path, 16*diskBlockSize)
	for block, expected := range written {
		second.registers[w1] = block
		second.registers[w2] = diskBlockSize
		second.execute(Instruction{opcode: DREAD, rs1: w1, rs2: w2})
		if second.registers[ecr] == faultExc {
			t.Fatalf("block %d: unexpected fault", block)
		}
		if !bytes.Equal(second.memory[diskBlockSize:2*diskBlockSize], expected) {
			t.Errorf("block %d: the second machine did not read what the first wrote", block)
		}
	}
	if !bytes.Equal(diskBlock(second, 5), make([]byte, diskBlockSize)) {
		t.Errorf("Expected a block nobody wrote to stay zeroed")
	}
}

func TestAttachDiskFileErrors(t *testing.T) {
	existing := make([]byte, 4*diskBlockSize)
	fillPattern(existing, 0x77)

	t.Run("existing file with a different size is left untouched", func(t *testing.T) {
		for _, size := range []int{diskBlockSize, 3 * diskBlockSize, 5 * diskBlockSize, 8 * diskBlockSize} {
			path := filepath.Join(t.TempDir(), "disk.img")
			if err := os.WriteFile(path, existing, 0o644); err != nil {
				t.Fatal(err)
			}
			machine := NewMachineWithDisk(diskTestMemory, 0, false)

			if err := machine.AttachDiskFile(path, size); err == nil {
				t.Errorf("size %d: expected an error for a file of %d bytes", size, len(existing))
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(after, existing) {
				t.Errorf("size %d: the file was modified", size)
			}
			if len(machine.disk) != 0 {
				t.Errorf("size %d: expected no disk to be attached", size)
			}
		}
	})

	t.Run("invalid sizes do not create the file", func(t *testing.T) {
		for _, size := range []int{0, -diskBlockSize, 1, diskBlockSize - 1, diskBlockSize + 1, 3*diskBlockSize + 4} {
			path := filepath.Join(t.TempDir(), "disk.img")
			machine := NewMachineWithDisk(diskTestMemory, 0, false)

			if err := machine.AttachDiskFile(path, size); err == nil {
				t.Errorf("size %d: expected an error", size)
			}
			if _, err := os.Stat(path); err == nil {
				t.Errorf("size %d: the file was created", size)
			}
		}
	})

	t.Run("unusable paths", func(t *testing.T) {
		dir := t.TempDir()
		for name, path := range map[string]string{
			"missing directory": filepath.Join(dir, "no", "such", "disk.img"),
			"a directory":       dir,
		} {
			machine := NewMachineWithDisk(diskTestMemory, 0, false)
			if err := machine.AttachDiskFile(path, 4*diskBlockSize); err == nil {
				t.Errorf("%s: expected an error", name)
			}
		}
	})
}

func TestAttachDiskFileEmptyExistingFileIsSized(t *testing.T) {
	path := filepath.Join(t.TempDir(), "disk.img")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	machine := newDiskFileMachine(t, path, 3*diskBlockSize)

	info, _ := os.Stat(path)
	if info.Size() != 3*diskBlockSize || len(machine.disk) != 3*diskBlockSize {
		t.Errorf("Expected file and disk of %d bytes, got file %d and disk %d", 3*diskBlockSize, info.Size(), len(machine.disk))
	}
}

func TestFaultingTransfersLeaveTheDiskFileUntouched(t *testing.T) {
	for _, opcode := range []Opcode{DREAD, DWRITE} {
		for _, tt := range []struct {
			name        string
			block, addr uint32
		}{
			{"block equal to disk size", 4, 0},
			{"block far past disk size", 0xFFFFFFFF, 0},
			{"unaligned buffer", 0, 4},
			{"buffer past end of memory", 0, diskTestMemory},
		} {
			path := filepath.Join(t.TempDir(), "disk.img")
			machine := newDiskFileMachine(t, path, 4*diskBlockSize)
			fillPattern(machine.memory, 0x11)
			fillPattern(machine.disk, 0x22)
			before, _ := os.ReadFile(path)

			machine.registers[esa] = 0x800
			machine.registers[w1] = tt.block
			machine.registers[w2] = tt.addr
			machine.execute(Instruction{opcode: opcode, rs1: w1, rs2: w2})

			if machine.registers[ecr] != faultExc {
				t.Errorf("opcode %d, %s: expected faultExc, got %d", opcode, tt.name, machine.registers[ecr])
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(after, before) {
				t.Errorf("opcode %d, %s: the file changed", opcode, tt.name)
			}
		}
	}
}

func TestSyncDiskWithoutFile(t *testing.T) {
	for _, machine := range []*Machine{NewMachine(diskTestMemory, false), NewMachineWithDisk(diskTestMemory, 0, false)} {
		if err := machine.SyncDisk(); err != nil {
			t.Errorf("Expected syncing an in-memory disk to be a no-op, got %v", err)
		}
	}
}
