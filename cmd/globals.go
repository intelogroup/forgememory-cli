package main

import (
	"os"

	"github.com/forge/forge/internal/service"
)

var (
	serviceNew         = service.New
	serviceIsInstalled = func(m *service.Manager) bool { return m.IsServiceInstalled() }
	serviceInstall     = func(m *service.Manager) error { return m.Install() }
	serviceUninstall   = func(m *service.Manager) error { return m.Uninstall() }
	serviceStart       = func(m *service.Manager) error { return m.Start() }
	serviceStop        = func(m *service.Manager) error { return m.Stop() }
	exitWithCode       = os.Exit
)

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func contains(slice []string, item string) bool {
	for _, v := range slice {
		if v == item {
			return true
		}
	}
	return false
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

func prefix(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
