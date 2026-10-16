//go:build darwin || linux

package main

import (
	"errors"
	"os"
	"sort"
	"syscall"
)

// All supported writers hold a shared workspace lock; export/cleanup take an
// exclusive nonblocking lock. Run locks continue to serialize ordinary writers.
func lockWorkspaces(dirs []string, exclusive bool) (func(), error) {
	sort.Strings(dirs)
	held := []*os.File{}
	unlock := func() {
		for i := len(held) - 1; i >= 0; i-- {
			_ = syscall.Flock(int(held[i].Fd()), syscall.LOCK_UN)
			_ = held[i].Close()
		}
	}
	seen := map[string]bool{}
	for _, dir := range dirs {
		if seen[dir] {
			continue
		}
		seen[dir] = true
		root, err := os.OpenRoot(dir)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			unlock()
			return nil, err
		}
		f, err := root.Open(".")
		root.Close()
		if err != nil {
			unlock()
			return nil, err
		}
		mode := syscall.LOCK_SH
		if exclusive {
			mode = syscall.LOCK_EX | syscall.LOCK_NB
		}
		if err := syscall.Flock(int(f.Fd()), mode); err != nil {
			f.Close()
			unlock()
			return nil, errors.New("рабочая область занята активной операцией; повторите после её завершения")
		}
		held = append(held, f)
	}
	return unlock, nil
}

func lockCommand(args []string) (func(), error) {
	empty := func() {}
	if len(args) < 2 {
		return empty, nil
	}
	configs := []string{}
	switch args[0] {
	case "external":
		if len(args) == 4 {
			configs = append(configs, args[2])
		}
	case "corpus", "publish", "rerun":
		if len(args) > 2 {
			configs = args[2:]
		}
	case "reconcile", "check", "index", "anchors", "cite", "prepare", "tasks", "status", "report", "review", "review-check", "draft", "submit", "validate", "retry", "test", "php-facts", "php-typed", "plan":
		configs = []string{args[1]}
	default:
		return empty, nil
	}
	dirs := []string{}
	for _, path := range configs {
		cfg, err := loadConfig(path, true)
		if err != nil {
			return nil, err
		}
		dirs = append(dirs, cfg.ReportsDir)
	}
	return lockWorkspaces(dirs, false)
}
