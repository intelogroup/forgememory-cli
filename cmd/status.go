package main

import ()

func runStatus(args []string) {
	jsonOutput := false
	detailed := false
	for _, a := range args {
		if a == "--json" {
			jsonOutput = true
			break
		}
		if a == "--detailed" {
			detailed = true
		}
	}
	if jsonOutput {
		statusOutputJSON()
		return
	}
	statusOutput()
	if detailed {
		runHealth([]string{})
	}
}
