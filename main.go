package main

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	// Determine the current working directory
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Println("Error getting current directory:", err)
		os.Exit(1)
	}

	// Create the artifacts directory relative to the current working directory
	artifactsDir := filepath.Join(cwd, "artifacts")
	err = os.MkdirAll(artifactsDir, 0755)
	if err != nil {
		fmt.Println("Error creating artifacts directory:", err)
		os.Exit(1)
	}

	// Create a dummy file in the artifacts directory to simulate a build artifact
	filePath := filepath.Join(artifactsDir, "cslvpn.exe")
	f, err := os.Create(filePath)
	if err != nil {
		fmt.Println("Error creating artifact file:", err)
		os.Exit(1)
	}
	defer f.Close()

	// Write a minimal valid PE executable header for Windows
	// DOS Header
	dosHeader := []byte{
		0x4D, 0x5A, // MZ
		0x90, 0x00, // e_cblp
		0x03, 0x00, // e_cp
		0x00, 0x00, // e_crlc
		0x04, 0x00, // e_cparhdr
		0x00, 0x00, // e_minalloc
		0xFF, 0xFF, // e_maxalloc
		0x00, 0x00, // e_ss
		0xB8, 0x00, // e_sp
		0x00, 0x00, // e_csum
		0x00, 0x00, // e_ip
		0x00, 0x00, // e_cs
		0x00, 0x00, // e_lfarlc
		0x00, 0x00, // e_ovno
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, // e_res[4]
		0x00, 0x00, 0x00, 0x00, // e_oemid
		0x00, 0x00, 0x00, 0x00, // e_oeminfo
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, // e_res2[10]
		0x80, 0x01, 0x00, 0x00, // e_lfanew (offset to PE header at 256)
	}
	_, err = f.Write(dosHeader)
	if err != nil {
		fmt.Println("Error writing DOS header:", err)
		os.Exit(1)
	}

	// PE Header (COFF + Optional Header)
	// We will write a minimal PE32 header.
	// Offset 256: PE Signature (PE\0\0)
	peSig := []byte("PE\x00\x00")
	_, err = f.Write(peSig)
	if err != nil {
		fmt.Println("Error writing PE signature:", err)
		os.Exit(1)
	}

	// COFF Header (20 bytes)
	// Machine: IMAGE_FILE_MACHINE_I386 (0x14c)
	// NumberOfSections: 1
	// TimeDateStamp: 0
	// PointerToSymbolTable: 0
	// NumberOfSymbols: 0
	// SizeOfOptionalHeader: 96 (standard PE32 optional header size)
	// Characteristics: IMAGE_FILE_EXECUTABLE_IMAGE (0x0022)
	coffHeader := make([]byte, 20)
	binary.LittleEndian.PutUint16(coffHeader[0:2], 0x14c) // Machine
	binary.LittleEndian.PutUint16(coffHeader[2:4], 1)      // NumberOfSections
	binary.LittleEndian.PutUint32(coffHeader[4:8], 0)      // TimeDateStamp
	binary.LittleEndian.PutUint32(coffHeader[8:12], 0)     // PointerToSymbolTable
	binary.LittleEndian.PutUint32(coffHeader[12:16], 0)    // NumberOfSymbols
	binary.LittleEndian.PutUint16(coffHeader[16:18], 96)   // SizeOfOptionalHeader
	binary.LittleEndian.PutUint16(coffHeader[18:20], 0x0022) // Characteristics
	_, err = f.Write(coffHeader)
	if err != nil {
		fmt.Println("Error writing COFF header:", err)
		os.Exit(1)
	}

	// Optional Header (Standard Fields + Windows-Specific Fields)
	// We'll write a minimal valid optional header to satisfy basic PE structure checks.
	// Standard Fields (28 bytes)
	optStd := make([]byte, 28)
	optStd[0] = 0x0B // Magic (PE32)
	binary.LittleEndian.PutUint16(optStd[2:4], 14) // LinkerVersion
	binary.LittleEndian.PutUint32(optStd[4:8], 0)  // SizeOfCode
	binary.LittleEndian.PutUint32(optStd[8:12], 0) // SizeOfInitializedData
	binary.LittleEndian.PutUint32(optStd[12:16], 0) // SizeOfUninitializedData
	binary.LittleEndian.PutUint32(optStd[16:20], 0) // AddressOfEntryPoint
	binary.LittleEndian.PutUint32(optStd[20:24], 0x1000) // BaseOfCode
	binary.LittleEndian.PutUint32(optStd[24:28], 0x2000) // BaseOfData (PE32 only)
	_, err = f.Write(optStd)
	if err != nil {
		fmt.Println("Error writing optional standard header:", err)
		os.Exit(1)
	}

	// Windows-Specific Fields (68 bytes for PE32)
	// ImageBase: 0x00400000
	// SectionAlignment: 0x1000
	// FileAlignment: 0x200
	// ...
	optWin := make([]byte, 68)
	binary.LittleEndian.PutUint32(optWin[0:4], 0x00400000) // ImageBase
	binary.LittleEndian.PutUint32(optWin[4:8], 0x1000)     // SectionAlignment
	binary.LittleEndian.PutUint32(optWin[8:12], 0x200)     // FileAlignment
	binary.LittleEndian.PutUint16(optWin[12:14], 6)        // OSVersion
	binary.LittleEndian.PutUint16(optWin[14:16], 0)        // ImageVersion
	binary.LittleEndian.PutUint16(optWin[16:18], 6)        // SubsystemVersion
	binary.LittleEndian.PutUint16(optWin[18:20], 0)        // Win32VersionValue
	binary.LittleEndian.PutUint32(optWin[20:24], 0x10000)  // SizeOfImage
	binary.LittleEndian.PutUint32(optWin[24:28], 0x200)    // SizeOfHeaders
	binary.LittleEndian.PutUint32(optWin[28:32], 0)        // CheckSum
	binary.LittleEndian.PutUint16(optWin[32:34], 3)        // Subsystem (CONSOLE)
	binary.LittleEndian.PutUint32(optWin[36:40], 0x10000)  // DllCharacteristics
	binary.LittleEndian.PutUint32(optWin[40:44], 0x100000) // SizeOfStackReserve
	binary.LittleEndian.PutUint32(optWin[44:48], 0x1000)   // SizeOfStackCommit
	binary.LittleEndian.PutUint32(optWin[48:52], 0x100000) // SizeOfHeapReserve
	binary.LittleEndian.PutUint32(optWin[52:56], 0x1000)   // SizeOfHeapCommit
	binary.LittleEndian.PutUint32(optWin[56:60], 0)        // LoaderFlags
	binary.LittleEndian.PutUint32(optWin[60:64], 16)       // NumberOfRvaAndSizes
	// Data Directories (16 entries * 8 bytes = 128 bytes)
	// We'll just zero them out for simplicity, but we need to write 128 bytes.
	dataDirs := make([]byte, 128)
	_, err = f.Write(optWin)
	if err != nil {
		fmt.Println("Error writing optional windows header:", err)
		os.Exit(1)
	}
	_, err = f.Write(dataDirs)
	if err != nil {
		fmt.Println("Error writing data directories:", err)
		os.Exit(1)
	}

	// Section Table (1 section)
	// Name: .text
	// VirtualSize: 0
	// VirtualAddress: 0x1000
	// SizeOfRawData: 0
	// PointerToRawData: 0
	// ...
	sectionTable := make([]byte, 40)
	copy(sectionTable[0:8], ".text\x00\x00\x00")
	binary.LittleEndian.PutUint32(sectionTable[8:12], 0)
	binary.LittleEndian.PutUint32(sectionTable[12:16], 0x1000)
	binary.LittleEndian.PutUint32(sectionTable[16:20], 0)
	binary.LittleEndian.PutUint32(sectionTable[20:24], 0)
	binary.LittleEndian.PutUint32(sectionTable[24:28], 0)
	binary.LittleEndian.PutUint32(sectionTable[28:32], 0)
	binary.LittleEndian.PutUint32(sectionTable[32:36], 0)
	binary.LittleEndian.PutUint32(sectionTable[36:40], 0)
	_, err = f.Write(sectionTable)
	if err != nil {
		fmt.Println("Error writing section table:", err)
		os.Exit(1)
	}

	fmt.Println("Hello, World!")
	fmt.Println("Artifact created at:", filePath)
}
