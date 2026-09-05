package vm

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

func processIdentity(pid int) (string, error) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("inspect PID %d: %w", pid, err)
	}
	// comm is parenthesized and can itself contain spaces and parentheses.
	end := strings.LastIndexByte(string(data), ')')
	if end < 0 {
		return "", fmt.Errorf("invalid process stat for PID %d", pid)
	}
	fields := strings.Fields(string(data[end+1:]))
	if len(fields) < 20 {
		return "", fmt.Errorf("incomplete process stat for PID %d", pid)
	}
	if fields[0] == "Z" || fields[0] == "X" {
		return "", nil
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", fmt.Errorf("read host boot identity: %w", err)
	}
	return strings.TrimSpace(string(boot)) + ":" + fields[19], nil
}
