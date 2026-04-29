package main

import (
	"fmt"
	"os"
)

func runServiceInstall(args []string) {
	fmt.Println("Installing Forge as system service...")

	mgr, err := serviceNew()
	if err != nil {
		fmt.Fprintf(os.Stderr, "  Error: %v\n", err)
		exitWithCode(1)
	}

	if serviceIsInstalled(mgr) {
		fmt.Println("  Service already installed.")
		return
	}

	if err := serviceInstall(mgr); err != nil {
		fmt.Fprintf(os.Stderr, "  Error installing service: %v\n", err)
		exitWithCode(1)
	}

	fmt.Println("  Service installed successfully.")
	fmt.Println("  Run 'forge service-start' to start the daemon as a service.")
}

func runServiceUninstall(args []string) {
	fmt.Println("Uninstalling Forge service...")

	mgr, err := serviceNew()
	if err != nil {
		fmt.Fprintf(os.Stderr, "  Error: %v\n", err)
		exitWithCode(1)
	}

	if !serviceIsInstalled(mgr) {
		fmt.Println("  Service not installed.")
		return
	}

	if err := serviceUninstall(mgr); err != nil {
		fmt.Fprintf(os.Stderr, "  Error uninstalling service: %v\n", err)
		exitWithCode(1)
	}

	fmt.Println("  Service uninstalled successfully.")
}

func runServiceStart(args []string) {
	fmt.Println("Starting Forge service...")

	mgr, err := serviceNew()
	if err != nil {
		fmt.Fprintf(os.Stderr, "  Error: %v\n", err)
		exitWithCode(1)
	}

	if err := serviceStart(mgr); err != nil {
		fmt.Fprintf(os.Stderr, "  Error starting service: %v\n", err)
		exitWithCode(1)
	}

	fmt.Println("  Service started.")
}

func runServiceStop(args []string) {
	fmt.Println("Stopping Forge service...")

	mgr, err := serviceNew()
	if err != nil {
		fmt.Fprintf(os.Stderr, "  Error: %v\n", err)
		exitWithCode(1)
	}

	if err := serviceStop(mgr); err != nil {
		fmt.Fprintf(os.Stderr, "  Error stopping service: %v\n", err)
		exitWithCode(1)
	}

	fmt.Println("  Service stopped.")
}
