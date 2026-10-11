//go:build !linux

package main

// sandboxd only runs as a container PID 1 on Linux; other platforms are for local tests.

func hardenProcess() {}

func runInit() int { return 1 }
