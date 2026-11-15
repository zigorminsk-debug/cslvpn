package main

import (
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	// Determine the directory where the executable resides
	execPath, err := os.Executable()
	if err != nil {
		fmt.Println("Error getting executable path:", err)
		os.Exit(1)
	}
	execDir := filepath.Dir(execPath)

	// Create the artifacts directory relative to the executable
	artifactsDir := filepath.Join(execDir, "artifacts")
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

	// Write some content to the file
	_, err = f.WriteString("This is a dummy Windows executable artifact.")
	if err != nil {
		fmt.Println("Error writing to artifact file:", err)
		os.Exit(1)
	}

	fmt.Println("Hello, World!")
	fmt.Println("Artifact created at:", filePath)
}
