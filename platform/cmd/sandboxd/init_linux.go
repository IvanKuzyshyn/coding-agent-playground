package main

import (
	"os"
	"os/signal"
	"syscall"
)

// hardenProcess marks the process non-dumpable. Unsetenv does not change /proc/<pid>/environ,
// which same-uid children could otherwise read to recover SANDBOXD_TOKEN.
func hardenProcess() {
	const prSetDumpable = 4
	syscall.RawSyscall(syscall.SYS_PRCTL, prSetDumpable, 0, 0)
}

// runInit runs when sandboxd is the container's PID 1. Orphaned descendants of agent commands
// are reparented to PID 1 and would stay zombies (counting against --pids-limit) unless reaped,
// and a blanket reaper would steal exit statuses from exec.Cmd.Wait. So PID 1 re-executes
// sandboxd as its child, forwards signals to it, reaps everything and exits with its status.
func runInit() int {
	sigs := make(chan os.Signal, 8)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP, syscall.SIGQUIT)

	exe, err := os.Executable()
	if err != nil {
		os.Stderr.WriteString("sandboxd init: " + err.Error() + "\n")
		return 1
	}
	child, err := os.StartProcess(exe, os.Args, &os.ProcAttr{
		Env:   os.Environ(),
		Files: []*os.File{os.Stdin, os.Stdout, os.Stderr},
	})
	if err != nil {
		os.Stderr.WriteString("sandboxd init: " + err.Error() + "\n")
		return 1
	}
	os.Unsetenv(tokenEnv)

	go func() {
		for sig := range sigs {
			child.Signal(sig)
		}
	}()
	for {
		var ws syscall.WaitStatus
		pid, err := syscall.Wait4(-1, &ws, 0, nil)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return 1
		}
		if pid == child.Pid {
			if ws.Signaled() {
				return 128 + int(ws.Signal())
			}
			return ws.ExitStatus()
		}
	}
}
