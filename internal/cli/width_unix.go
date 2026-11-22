//go:build unix

package cli

import (
	"os"

	"golang.org/x/sys/unix"
)

func terminalWidth(f *os.File) int {
	size, err := unix.IoctlGetWinsize(int(f.Fd()), unix.TIOCGWINSZ)
	if err != nil {
		return 0
	}
	return int(size.Col)
}
