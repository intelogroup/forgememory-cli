package main

import (
	"fmt"
	"strings"
)

func runHealth(args []string) {
	report, err := collectStatusReport()
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

	status := report.Distillation.LastStatus
	if status == "" {
		status = "pending"
	}
	fmt.Println("Distillation Health:")
	if report.Distillation.LastSuccessAt != "" {
		fmt.Printf("  Last distilled: %s\n", formatRelativeTime(report.Distillation.LastSuccessAt))
	} else {
		fmt.Println("  Last distilled: never")
	}
	fmt.Printf("  Status: %s\n", strings.ToUpper(status))
	fmt.Printf("  Failed attempts: %d\n", report.Distillation.ConsecutiveFailures)
	fmt.Printf("  Raw events queued: %d\n", report.Events.Undistilled)
	if report.Distillation.LastErrorMessage != "" {
		fmt.Printf("  Error: %s\n", report.Distillation.LastErrorMessage)
	}

	alerts := healthAlerts(report)
	if len(alerts) > 0 {
		fmt.Println("Alerts:")
		for _, a := range alerts {
			fmt.Printf("  - %s\n", a)
		}
	}
}

func healthAlerts(report statusReport) []string {
	var alerts []string
	if report.Distillation.ConsecutiveFailures >= 3 {
		alerts = append(alerts, "distillation_failed: 3+ consecutive failures")
	}
	if report.Distillation.LastErrorMessage != "" {
		alerts = append(alerts, "last_error: "+truncate(report.Distillation.LastErrorMessage, 120))
	}
	return alerts
}
