package utils

import (
	"os"
	"runtime"
)

func GetWorkDir() string {
	// try to get the current working directory
	// if failed, return the home directory
	dir, _ := os.Getwd()
	if dir == "" {
		dir = os.Getenv("HOME")
	}
	return dir
}

func GetSystemInfo() string {
	return runtime.GOOS + "/" + runtime.GOARCH
}
