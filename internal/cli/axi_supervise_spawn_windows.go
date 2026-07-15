//go:build windows

package cli

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

func spawnSupervisorWorker(nmHome, cwd, runID string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable: %w", err)
	}
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("open null device: %w", err)
	}
	defer devNull.Close()
	cmd := exec.Command(exe, "axi", "supervise", "worker", "--run", runID)
	cmd.Dir = cwd
	cmd.Env = supervisorEnv(nmHome)
	cmd.Stdout, cmd.Stderr, cmd.Stdin = devNull, devNull, nil
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
