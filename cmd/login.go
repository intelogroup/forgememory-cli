package main

import (
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/forge/forge/internal/config"
)

func openBrowser(url string) {
	var args []string
	switch runtime.GOOS {
	case "darwin":
		args = []string{"open", url}
	case "windows":
		args = []string{"cmd", "/c", "start", url}
	default:
		args = []string{"xdg-open", url}
	}
	exec.Command(args[0], args[1:]...).Start()
}

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func runLogin(args []string) {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	purchaseFlag := fs.Bool("purchase", false, "Purchase credits after login")
	// kept for backward compat, unused
	fs.String("email", "", "Email address (unused; login uses browser)")
	fs.String("password", "", "Password (unused; login uses browser)")
	fs.Bool("signup", false, "Open signup page (unused)")
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	serverURL := os.Getenv("FORGE_BASE_URL")
	if serverURL == "" {
		serverURL = "https://forgememo-server.onrender.com"
	}

	// Start local callback listener on a random port.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: cannot start local listener: %v\n", err)
		os.Exit(1)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	callbackURL := fmt.Sprintf("http://127.0.0.1:%d/callback", port)
	state := randomHex(16)

	tokenCh := make(chan string, 1)
	errCh := make(chan error, 1)

	mux := http.NewServeMux()
	srv := &http.Server{Handler: mux}
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("state") != state {
			http.Error(w, "state mismatch", http.StatusBadRequest)
			errCh <- fmt.Errorf("OAuth state mismatch — possible CSRF")
			return
		}
		token := r.URL.Query().Get("token")
		if token == "" {
			http.Error(w, "missing token", http.StatusBadRequest)
			errCh <- fmt.Errorf("no token in callback")
			return
		}
		fmt.Fprint(w, "<html><body><h2>Logged in! You can close this tab.</h2></body></html>")
		tokenCh <- token
	})
	go srv.Serve(listener)

	authURL := fmt.Sprintf("%s/cli-auth?callback=%s&state=%s",
		serverURL,
		url.QueryEscape(callbackURL),
		state,
	)

	fmt.Println("Opening browser for login...")
	fmt.Printf("  %s\n\n", authURL)
	openBrowser(authURL)
	fmt.Println("Waiting for login (timeout: 5 min)...")

	select {
	case token := <-tokenCh:
		srv.Close()
		cfg := config.Config{
			Provider: "forgememo",
			APIKey:   token,
			BaseURL:  serverURL,
		}
		if err := config.Save(cfg); err != nil {
			fmt.Fprintf(os.Stderr, "Error saving config: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Logged in successfully.")
		if *purchaseFlag {
			// Start a fresh callback server for the payment flow.
			payListener, lerr := net.Listen("tcp", "127.0.0.1:0")
			if lerr != nil {
				fmt.Fprintf(os.Stderr, "Error: cannot start payment listener: %v\n", lerr)
				os.Exit(1)
			}
			payPort := payListener.Addr().(*net.TCPAddr).Port
			payCallbackURL := fmt.Sprintf("http://127.0.0.1:%d/callback", payPort)
			payState := randomHex(16)
			payMux := http.NewServeMux()
			paySrv := &http.Server{Handler: payMux}
			payDone := make(chan struct{})
			payMux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Access-Control-Allow-Origin", "*")
				w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
				if r.Method == http.MethodOptions {
					w.WriteHeader(http.StatusNoContent)
					return
				}
				if r.URL.Query().Get("state") != payState {
					http.Error(w, "state mismatch", http.StatusBadRequest)
					return
				}
				fmt.Fprint(w, "<html><body><h2>Payment complete! You can close this tab.</h2></body></html>")
				fmt.Println("Payment confirmed. forge is ready.")
				// Keep server alive briefly to handle React strict-mode double-fetch in dev.
				go func() {
					time.Sleep(3 * time.Second)
					paySrv.Close()
				}()
				// Only close payDone once.
				select {
				case <-payDone:
				default:
					close(payDone)
				}
			})
			go paySrv.Serve(payListener)
			billingURL := fmt.Sprintf("%s/billing/cli-setup?token=%s&cli_callback=%s&state=%s",
				serverURL,
				url.QueryEscape(token),
				url.QueryEscape(payCallbackURL),
				payState,
			)
			fmt.Println("Opening browser for payment...")
			fmt.Printf("  %s\n\n", billingURL)
			openBrowser(billingURL)
			fmt.Println("Waiting for payment (timeout: 10 min)...")
			select {
			case <-payDone:
			case <-time.After(10 * time.Minute):
				paySrv.Close()
				fmt.Fprintf(os.Stderr, "Payment timed out. Run 'forge billing' to purchase credits.\n")
			}
		}
	case e := <-errCh:
		srv.Close()
		fmt.Fprintf(os.Stderr, "Error: %v\n", e)
		os.Exit(1)
	case <-time.After(5 * time.Minute):
		srv.Close()
		fmt.Fprintf(os.Stderr, "Error: login timed out\n")
		os.Exit(1)
	}
}
