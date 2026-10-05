package machine

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
	assembler "zepa-machine/cross-assembler"
)

const (
	diskTestMemory = 64 * 1024
	testDiskBlocks = 1024
)

func fillPattern(buf []byte, seed byte) {
	for i := range buf {
		buf[i] = seed + byte(i*7)
	}
}

func diskBlock(m *Machine, block int) []byte {
	return m.disk[block*diskBlockSize : (block+1)*diskBlockSize]
}

func newDiskMachine() *Machine {
	return NewMachineWithDisk(diskTestMemory, testDiskBlocks*diskBlockSize, false)
}

func TestNewMachineDiskSize(t *testing.T) {
	tests := []struct {
		name          string
		machine       *Machine
		expectedBytes int
	}{
		{"default disk smaller than a block", NewMachine(2047, false), 0},
		{"default disk of exactly one block", NewMachine(2048, false), diskBlockSize},
		{"default disk twice a block-aligned memory", NewMachine(diskTestMemory, false), 2 * diskTestMemory},
		{"default disk rounds down to a block", NewMachine(diskBlockSize+4, false), 2 * diskBlockSize},
		{"default disk of a 1MB memory", NewMachine(1<<20, false), 2 << 20},
		{"explicit disk bigger than memory", NewMachineWithDisk(diskTestMemory, 1<<20, false), 1 << 20},
		{"explicit disk smaller than memory", NewMachineWithDisk(diskTestMemory, diskBlockSize, false), diskBlockSize},
		{"explicit disk with no blocks", NewMachineWithDisk(diskTestMemory, 0, false), 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if len(tt.machine.disk) != tt.expectedBytes {
				t.Fatalf("Expected disk of %d bytes, got %d", tt.expectedBytes, len(tt.machine.disk))
			}
			if !bytes.Equal(tt.machine.disk, make([]byte, len(tt.machine.disk))) {
				t.Errorf("Expected a fresh disk to be zeroed")
			}
		})
	}
}

func TestDiskTransferHonorsDiskSize(t *testing.T) {
	for _, blocks := range []int{1, 2, 17, 2048} {
		for _, opcode := range []Opcode{DREAD, DWRITE} {
			machine := NewMachineWithDisk(diskTestMemory, blocks*diskBlockSize, false)
			fillPattern(machine.memory, 0x11)
			fillPattern(machine.disk, 0x22)
			memoryBefore := bytes.Clone(machine.memory)
			diskBefore := bytes.Clone(machine.disk)

			machine.registers[esa] = 0x800
			machine.registers[w1] = uint32(blocks)
			machine.registers[w2] = 0
			machine.execute(Instruction{opcode: opcode, rs1: w1, rs2: w2})

			if machine.registers[ecr] != faultExc {
				t.Errorf("%d blocks, opcode %d: expected block %d to fault, got ecr %d", blocks, opcode, blocks, machine.registers[ecr])
			}
			if !bytes.Equal(machine.memory, memoryBefore) || !bytes.Equal(machine.disk, diskBefore) {
				t.Errorf("%d blocks, opcode %d: expected memory and disk unchanged", blocks, opcode)
			}
		}
	}
}

func TestDiskLastBlockRoundTrip(t *testing.T) {
	for _, blocks := range []int{1, 2, 17, 2048} {
		machine := NewMachineWithDisk(diskTestMemory, blocks*diskBlockSize, false)
		last := uint32(blocks - 1)
		fillPattern(machine.memory[0:diskBlockSize], byte(blocks))
		original := bytes.Clone(machine.memory[0:diskBlockSize])

		machine.registers[w1] = last
		machine.registers[w2] = 0
		machine.execute(Instruction{opcode: DWRITE, rs1: w1, rs2: w2})

		clear(machine.memory[0:diskBlockSize])
		machine.execute(Instruction{opcode: DREAD, rs1: w1, rs2: w2})

		if machine.registers[ecr] == faultExc {
			t.Errorf("%d blocks: last block %d unexpectedly faulted", blocks, last)
		}
		if !bytes.Equal(machine.memory[0:diskBlockSize], original) {
			t.Errorf("%d blocks: last block did not round trip", blocks)
		}
		if !bytes.Equal(diskBlock(machine, int(last)), original) {
			t.Errorf("%d blocks: disk block %d does not hold the written data", blocks, last)
		}
	}
}

func TestDiskWithNoBlocksAlwaysFaults(t *testing.T) {
	for _, opcode := range []Opcode{DREAD, DWRITE} {
		for _, block := range []uint32{0, 1, 0xFFFFFFFF} {
			machine := NewMachineWithDisk(2048, 0, false)
			machine.registers[esa] = 0x800
			machine.registers[w1] = block
			machine.registers[w2] = 0
			machine.execute(Instruction{opcode: opcode, rs1: w1, rs2: w2})

			if machine.registers[ecr] != faultExc {
				t.Errorf("opcode %d block %d: expected faultExc, got %d", opcode, block, machine.registers[ecr])
			}
		}
	}
}

func TestDREAD(t *testing.T) {
	for _, block := range []int{0, 1, 11, testDiskBlocks - 1} {
		for _, addr := range []uint32{0, diskBlockSize, 3 * diskBlockSize} {
			machine := newDiskMachine()
			fillPattern(diskBlock(machine, block), byte(block))
			machine.registers[w1] = uint32(block)
			machine.registers[w2] = addr

			cycles := machine.execute(Instruction{opcode: DREAD, rs1: w1, rs2: w2})

			if cycles != diskAccessCycles {
				t.Errorf("block %d addr 0x%x: expected %d cycles, got %d", block, addr, diskAccessCycles, cycles)
			}
			if !bytes.Equal(machine.memory[addr:addr+diskBlockSize], diskBlock(machine, block)) {
				t.Errorf("block %d addr 0x%x: memory does not match the disk block", block, addr)
			}
			if !bytes.Equal(machine.memory[:addr], make([]byte, addr)) ||
				!bytes.Equal(machine.memory[addr+diskBlockSize:], make([]byte, diskTestMemory-addr-diskBlockSize)) {
				t.Errorf("block %d addr 0x%x: DREAD wrote outside the destination frame", block, addr)
			}
			if machine.registers[ecr] == faultExc || machine.registers[ecr] == pageFaultExc {
				t.Errorf("block %d addr 0x%x: unexpected exception %d", block, addr, machine.registers[ecr])
			}
		}
	}
}

func TestDWRITE(t *testing.T) {
	for _, block := range []int{0, 1, 11, testDiskBlocks - 1} {
		for _, addr := range []uint32{0, diskBlockSize, 3 * diskBlockSize} {
			machine := newDiskMachine()
			fillPattern(machine.memory[addr:addr+diskBlockSize], byte(block)+1)
			machine.registers[w3] = uint32(block)
			machine.registers[w4] = addr

			cycles := machine.execute(Instruction{opcode: DWRITE, rs1: w3, rs2: w4})

			if cycles != diskAccessCycles {
				t.Errorf("block %d addr 0x%x: expected %d cycles, got %d", block, addr, diskAccessCycles, cycles)
			}
			if !bytes.Equal(diskBlock(machine, block), machine.memory[addr:addr+diskBlockSize]) {
				t.Errorf("block %d addr 0x%x: disk block does not match memory", block, addr)
			}
			start, end := block*diskBlockSize, (block+1)*diskBlockSize
			if !bytes.Equal(machine.disk[:start], make([]byte, start)) ||
				!bytes.Equal(machine.disk[end:], make([]byte, len(machine.disk)-end)) {
				t.Errorf("block %d addr 0x%x: DWRITE wrote outside the target block", block, addr)
			}
		}
	}
}

func TestDWRITEThenDREADRoundTrip(t *testing.T) {
	machine := newDiskMachine()
	fillPattern(machine.memory[0:diskBlockSize], 0x42)
	original := bytes.Clone(machine.memory[0:diskBlockSize])

	machine.registers[w1] = 500
	machine.registers[w2] = 0
	machine.execute(Instruction{opcode: DWRITE, rs1: w1, rs2: w2})

	clear(machine.memory[0:diskBlockSize])
	machine.registers[w2] = 5 * diskBlockSize
	machine.execute(Instruction{opcode: DREAD, rs1: w1, rs2: w2})

	if !bytes.Equal(machine.memory[5*diskBlockSize:6*diskBlockSize], original) {
		t.Errorf("Expected the block read back to equal the block written")
	}
}

func TestDiskTransferFaults(t *testing.T) {
	tests := []struct {
		name      string
		block     uint32
		addr      uint32
		expectEFA bool
	}{
		{"block equal to disk size", testDiskBlocks, 0, false},
		{"block far past disk size", 0xFFFFFFFF, 0, false},
		{"unaligned buffer by 1", 0, 1, true},
		{"unaligned buffer by 4", 0, 4, true},
		{"unaligned buffer mid page", 0, diskBlockSize + 2048, true},
		{"buffer past end of memory", 0, diskTestMemory, true},
		{"buffer far past end of memory", 0, 0xFFFFF000, true},
	}

	for _, opcode := range []Opcode{DREAD, DWRITE} {
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				machine := newDiskMachine()
				fillPattern(machine.memory, 0x11)
				fillPattern(machine.disk, 0x22)
				memoryBefore := bytes.Clone(machine.memory)
				diskBefore := bytes.Clone(machine.disk)

				machine.registers[esa] = 0x800
				machine.registers[pc] = 0x100
				machine.registers[w1] = tt.block
				machine.registers[w2] = tt.addr

				machine.execute(Instruction{opcode: opcode, rs1: w1, rs2: w2})

				if machine.registers[ecr] != faultExc {
					t.Errorf("Expected ecr to be faultExc (%d), got %d", faultExc, machine.registers[ecr])
				}
				if machine.registers[pc] != 0x800 {
					t.Errorf("Expected pc to jump to esa 0x800, got 0x%x", machine.registers[pc])
				}
				if machine.registers[epc] != 0x100 {
					t.Errorf("Expected epc to be 0x100, got 0x%x", machine.registers[epc])
				}
				if tt.expectEFA && machine.registers[efa] != tt.addr {
					t.Errorf("Expected efa to be 0x%x, got 0x%x", tt.addr, machine.registers[efa])
				}
				if !bytes.Equal(machine.memory, memoryBefore) {
					t.Errorf("Expected memory unchanged after a faulting transfer")
				}
				if !bytes.Equal(machine.disk, diskBefore) {
					t.Errorf("Expected disk unchanged after a faulting transfer")
				}
			})
		}
	}
}

// setupKernelMMU enables the MMU in kernel mode with a kernel page table at
// physical address 0 mapping kernel page i to the frame in frames[i]
// (0 = unmapped).
func setupKernelMMU(m *Machine, frames []uint32) {
	m.registers[kptr] = 0
	for i, frame := range frames {
		pte := uint32(0)
		if frame != 0 {
			pte = isPteMappedMask | frame
		}
		binary.LittleEndian.PutUint32(m.memory[i*4:i*4+4], pte)
	}
	m.registers[sr] = 32 // MMU on, kernel mode (bit 3 clear)
}

func TestDREADWithMMUTranslatesKernelAddress(t *testing.T) {
	machine := newDiskMachine()
	// kernel page 2 -> frame 7, kernel page 3 -> frame 5
	setupKernelMMU(machine, []uint32{0, 0, 7, 5})
	fillPattern(diskBlock(machine, 9), 0x33)

	machine.registers[w1] = 9
	machine.registers[w2] = kernelBoundary + 2*pageSize
	machine.execute(Instruction{opcode: DREAD, rs1: w1, rs2: w2})

	if !bytes.Equal(machine.memory[7*pageSize:8*pageSize], diskBlock(machine, 9)) {
		t.Errorf("Expected block 9 in physical frame 7")
	}
	if !bytes.Equal(machine.memory[5*pageSize:6*pageSize], make([]byte, pageSize)) {
		t.Errorf("Expected frame 5 (next virtual page) untouched")
	}
	if machine.registers[ecr] == pageFaultExc || machine.registers[ecr] == faultExc {
		t.Errorf("Unexpected exception %d", machine.registers[ecr])
	}
}

func TestDWRITEWithMMUTranslatesKernelAddress(t *testing.T) {
	machine := newDiskMachine()
	setupKernelMMU(machine, []uint32{0, 0, 7, 5})
	fillPattern(machine.memory[5*pageSize:6*pageSize], 0x44)

	machine.registers[w1] = 3
	machine.registers[w2] = kernelBoundary + 3*pageSize
	machine.execute(Instruction{opcode: DWRITE, rs1: w1, rs2: w2})

	if !bytes.Equal(diskBlock(machine, 3), machine.memory[5*pageSize:6*pageSize]) {
		t.Errorf("Expected block 3 to hold physical frame 5")
	}
}

func TestDiskTransferUnmappedPageFaults(t *testing.T) {
	for _, opcode := range []Opcode{DREAD, DWRITE} {
		machine := newDiskMachine()
		setupKernelMMU(machine, []uint32{0, 0, 7, 5})
		fillPattern(machine.disk[:diskBlockSize], 0x55)
		memoryBefore := bytes.Clone(machine.memory)
		diskBefore := bytes.Clone(machine.disk)

		addr := uint32(kernelBoundary + 1*pageSize) // page 1 unmapped
		machine.registers[w1] = 0
		machine.registers[w2] = addr
		machine.execute(Instruction{opcode: opcode, rs1: w1, rs2: w2})

		if machine.registers[ecr] != pageFaultExc {
			t.Errorf("opcode %d: expected pageFaultExc (%d), got %d", opcode, pageFaultExc, machine.registers[ecr])
		}
		if machine.registers[efa] != addr {
			t.Errorf("opcode %d: expected efa 0x%x, got 0x%x", opcode, addr, machine.registers[efa])
		}
		if !bytes.Equal(machine.memory, memoryBefore) || !bytes.Equal(machine.disk, diskBefore) {
			t.Errorf("opcode %d: expected memory and disk unchanged", opcode)
		}
	}
}

func TestDiskInstructionsArePrivileged(t *testing.T) {
	machine := NewMachine(2048, false)

	for _, opcode := range []Opcode{DREAD, DWRITE} {
		machine.registers[sr] = 8 // user mode
		if !machine.checkIllegalInstruction(Instruction{opcode: opcode}) {
			t.Errorf("opcode %d: expected illegal in user mode", opcode)
		}

		machine.registers[sr] = 0 // kernel mode
		if machine.checkIllegalInstruction(Instruction{opcode: opcode}) {
			t.Errorf("opcode %d: expected legal in kernel mode", opcode)
		}
	}
}

// Assembles real source and runs it through fetch/decode/execute, so the
// opcode numbers of the assembler and of the simulator must agree.
func TestDiskInstructionsEndToEnd(t *testing.T) {
	source := "MV W5, #20\nMV W6, #0x2000\nDWRITE W5, W6\nMV W6, #0x3000\nDREAD W5, W6\n"
	instructions, err := assembler.LoadAssemblyFromReader(strings.NewReader(source))
	if err != nil {
		t.Fatalf("Error loading assembly: %v", err)
	}
	program, err := assembler.ConvertInstructionsToBinary(instructions)
	if err != nil {
		t.Fatalf("Error assembling: %v", err)
	}

	machine := newDiskMachine()
	machine.LoadProgram(program)
	fillPattern(machine.memory[0x2000:0x3000], 0x66)

	for i := 0; i < 5; i++ {
		if !machine.fetch() {
			t.Fatalf("fetch %d failed", i)
		}
		inst, ok := machine.decode()
		if !ok {
			t.Fatalf("decode %d failed (ir=0x%08x)", i, machine.registers[ir])
		}
		if i == 2 && (inst.opcode != DWRITE || inst.rs1 != w5 || inst.rs2 != w6) {
			t.Errorf("Expected DWRITE w5, w6, got %+v", inst)
		}
		if i == 4 && (inst.opcode != DREAD || inst.rs1 != w5 || inst.rs2 != w6) {
			t.Errorf("Expected DREAD w5, w6, got %+v", inst)
		}
		machine.execute(inst)
	}

	if !bytes.Equal(machine.memory[0x3000:0x4000], machine.memory[0x2000:0x3000]) {
		t.Errorf("Expected the block to be copied 0x2000 -> disk 20 -> 0x3000")
	}
	if !bytes.Equal(diskBlock(machine, 20), machine.memory[0x2000:0x3000]) {
		t.Errorf("Expected disk block 20 to hold the written data")
	}
}

func TestDisassembleDiskInstructions(t *testing.T) {
	for _, tt := range []struct {
		source, want string
	}{
		{"DREAD W1, W2", "DREAD w1, w2"},
		{"DWRITE W9, K0", "DWRITE w9, k0"},
	} {
		instructions, err := assembler.LoadAssemblyFromReader(strings.NewReader(tt.source + "\n"))
		if err != nil {
			t.Fatalf("Error loading assembly: %v", err)
		}
		program, err := assembler.ConvertInstructionsToBinary(instructions)
		if err != nil {
			t.Fatalf("Error assembling %q: %v", tt.source, err)
		}
		if got := decodeInstruction(binary.BigEndian.Uint32(program)); got != tt.want {
			t.Errorf("Expected %q, got %q", tt.want, got)
		}
	}
}
