//go:build kernel

package main

import (
	"encoding/binary"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	assembler "zepa-machine/cross-assembler"
	"zepa-machine/machine"
)

const (
	kernelMappingOffset = 0xC0000000 // kernel mapeado 3GB acima
	diskBlockSize       = 4096
	maxDiskSize         = 1 << 34
	diskFile            = "sackos.img"
)

func main() {
	if len(os.Args) < 3 || len(os.Args) > 4 {
		fmt.Println("Usage: go run -tags kernel . <memory_size> <time_slice> [disk_size]")
		return
	}

	sourceFile := "sackOS/kernel.asm"
	binaryCode, err := assembler.RunAssembler(sourceFile)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

	memorySize, err := parseMemorySize(os.Args[1])
	if err != nil {
		log.Fatalf("Conversion failed: %v", err)
	}

	if memorySize%4 != 0 {
		log.Fatalf("memory_size must be a multiple of 4, got %dB", memorySize)
	}
	if memorySize < (1<<30)+(1<<12) {
		log.Fatalf("memory_size must be at least 1GB + 4KB, got %dB", memorySize)
	}
	if memorySize > (1 << 32) {
		log.Fatalf("memory_size must be at most 4GB, got %dB", memorySize)
	}

	timeSlice, err := strconv.Atoi(os.Args[2])
	if err != nil {
		log.Fatalf("Conversion failed: %v", err)
	}

	diskArg := ""
	if len(os.Args) == 4 {
		diskArg = os.Args[3]
	}
	machine, err := newMachine(memorySize, diskArg, diskFile)
	if err != nil {
		log.Fatal(err)
	}
	machine.LoadProgram(binaryCode)

	memSlice := machine.GetMemory()[0x2000:0x2004]
	binary.LittleEndian.PutUint32(memSlice, uint32(256)) // max processes

	memSlice = machine.GetMemory()[0x2004:0x2008]
	binary.LittleEndian.PutUint32(memSlice, uint32(timeSlice))

	memSlice = machine.GetMemory()[0x2008:0x200C]
	binary.LittleEndian.PutUint32(memSlice, uint32(0x10000)) // buffer size

	go machine.Boot()
	runTUIWithMachine(machine)
	if err := machine.SyncDisk(); err != nil {
		log.Printf("could not sync %s: %v", diskFile, err)
	}
}

// newMachine backs the disk with the file at path. Without diskArg, the disk
// keeps the size of an existing file, or is twice as big as the memory.
func newMachine(memorySize int, diskArg, path string) (*machine.Machine, error) {
	diskSize := machine.DefaultDiskSize(memorySize)
	if diskArg != "" {
		var err error
		if diskSize, err = parseDiskSize(diskArg); err != nil {
			return nil, err
		}
	} else if info, err := os.Stat(path); err == nil && info.Size() > 0 {
		diskSize = int(info.Size())
	}

	m := machine.NewMachineWithDisk(memorySize, 0, true)
	if err := m.AttachDiskFile(path, diskSize); err != nil {
		return nil, err
	}
	return m, nil
}

func parseDiskSize(sizeStr string) (int, error) {
	diskSize, err := parseMemorySize(sizeStr)
	if err != nil {
		return 0, err
	}
	if diskSize < diskBlockSize || diskSize%diskBlockSize != 0 {
		return 0, fmt.Errorf("disk_size must be a multiple of 4KB and at least 4KB, got %dB", diskSize)
	}
	if diskSize > maxDiskSize {
		return 0, fmt.Errorf("disk_size must be at most 16GB, got %dB", diskSize)
	}
	return diskSize, nil
}

func parseMemorySize(sizeStr string) (int, error) {
	sizeStr = strings.ToUpper(strings.TrimSpace(sizeStr))
	multiplier := 1

	switch {
	case strings.HasSuffix(sizeStr, "GB"):
		multiplier = 1 << 30
		sizeStr = strings.TrimSuffix(sizeStr, "GB")
	case strings.HasSuffix(sizeStr, "MB"):
		multiplier = 1 << 20
		sizeStr = strings.TrimSuffix(sizeStr, "MB")
	case strings.HasSuffix(sizeStr, "KB"):
		multiplier = 1 << 10
		sizeStr = strings.TrimSuffix(sizeStr, "KB")
	case strings.HasSuffix(sizeStr, "B"):
		sizeStr = strings.TrimSuffix(sizeStr, "B")
	}

	val, err := strconv.Atoi(sizeStr)
	if err != nil {
		return 0, fmt.Errorf("invalid size format: %s", sizeStr)
	}

	return val * multiplier, nil
}
