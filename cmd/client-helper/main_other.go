//go:build !windows

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "DeskPatrol 提权 helper 仅支持 Windows")
	os.Exit(1)
}
