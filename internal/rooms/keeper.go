package rooms

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// SpawnKeeper starts `self keep`, a child that only blocks so a room entry can
// carry its pid (DESIGN D:188-195). Pdeathsig kills it when ircc dies.
// Pdeathsig fires when the parent OS thread exits, so call this from a goroutine
// locked (runtime.LockOSThread) for as long as ircc lives (D:193). The caller must
// Wait on the process after killing it, to reap it.
func SpawnKeeper(self string) (*os.Process, error) {
	if self == "" {
		return nil, errors.New("rooms: spawn keeper: empty executable path")
	}
	c := exec.Command(self, "keep")
	c.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	if err := c.Start(); err != nil {
		return nil, fmt.Errorf("rooms: spawn keeper: %w", err)
	}
	return c.Process, nil
}
