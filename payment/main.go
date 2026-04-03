package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/google/uuid"
)

var (
	supabaseURL   = os.Getenv("SUPABASE_URL")
	supabaseKey   = os.Getenv("SUPABASE_ANON_KEY")
	supabaseAdmin = os.Getenv("SUPABASE_SERVICE_KEY")
	stripeKey     = os.Getenv("STRIPE_SECRET_KEY")
	stripeWebhook = os.Getenv("STRIPE_WEBHOOK_SECRET")
)

const (
	CreditPrice    = 500 // $5.00 in cents
	CreditAmount   = 100 // credits per purchase
	InitialCredits = 5   // free credits for new users
)

type User struct {
	ID           string `json:"id"`
	Email        string `json:"email"`
	StripeCustID string `json:"stripe_customer_id"`
	Credits      int    `json:"credits"`
	APIKey       string `json:"api_key"`
	CreatedAt    string `json:"created_at"`
}

type Response struct {
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
	Data    any    `json:"data,omitempty"`
}

func main() {
	if supabaseURL == "" || supabaseKey == "" {
		log.Fatal("SUPABASE_URL and SUPABASE_ANON_KEY required")
	}

	http.HandleFunc("/health", handleHealth)
	http.HandleFunc("/api/login", handleLogin)
	http.HandleFunc("/api/register", handleRegister)
	http.HandleFunc("/api/credits", handleCredits)
	http.HandleFunc("/api/whoami", handleWhoami)
	http.HandleFunc("/api/checkout", handleCheckout)
	http.HandleFunc("/api/deduct", handleDeduct)
	http.HandleFunc("/api/distill", handleDistill)
	http.HandleFunc("/api/webhook", handleWebhook)

	port := os.Getenv("PORT")
	if port == "" {
		port = "3000"
	}
	log.Printf("Payment service starting on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	json.NewEncoder(w).Encode(Response{Success: true})
}

func handleRegister(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	json.NewDecoder(r.Body).Decode(&req)

	data := map[string]any{
		"email":    req.Email,
		"password": req.Password,
	}
	resp, err := supabasePost("/auth/v1/signup", data)
	if err != nil {
		json.NewEncoder(w).Encode(Response{Success: false, Message: err.Error()})
		return
	}

	var result struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
		Session struct {
			AccessToken string `json:"access_token"`
		} `json:"session"`
	}
	json.Unmarshal(resp, &result)

	if result.User.ID == "" {
		json.NewEncoder(w).Encode(Response{Success: false, Message: "registration failed"})
		return
	}

	user := User{
		ID:        result.User.ID,
		Email:     req.Email,
		Credits:   InitialCredits,
		APIKey:    uuid.New().String(),
		CreatedAt: time.Now().Format(time.RFC3339),
	}
	supabaseInsert("users", user)

	json.NewEncoder(w).Encode(Response{Success: true, Data: map[string]string{
		"token":   result.Session.AccessToken,
		"api_key": user.APIKey,
	}})
}

func handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	json.NewDecoder(r.Body).Decode(&req)

	data := map[string]any{
		"email":    req.Email,
		"password": req.Password,
	}
	resp, err := supabasePost("/auth/v1/token?grant_type=password", data)
	if err != nil {
		json.NewEncoder(w).Encode(Response{Success: false, Message: "invalid credentials"})
		return
	}

	var result struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
		AccessToken string `json:"access_token"`
	}
	json.Unmarshal(resp, &result)

	users := supabaseQuery("users", "id", result.User.ID)
	if len(users) == 0 {
		json.NewEncoder(w).Encode(Response{Success: false, Message: "user not found"})
		return
	}
	user := users[0]

	json.NewEncoder(w).Encode(Response{Success: true, Data: map[string]any{
		"token":   result.AccessToken,
		"api_key": user.APIKey,
		"credits": user.Credits,
	}})
}

func handleCredits(w http.ResponseWriter, r *http.Request) {
	apiKey := r.Header.Get("Authorization")
	if apiKey == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	users := supabaseQuery("users", "api_key", apiKey)
	if len(users) == 0 {
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}

	json.NewEncoder(w).Encode(Response{Success: true, Data: map[string]int{"credits": users[0].Credits}})
}

func handleWhoami(w http.ResponseWriter, r *http.Request) {
	apiKey := r.Header.Get("Authorization")
	if apiKey == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	users := supabaseQuery("users", "api_key", apiKey)
	if len(users) == 0 {
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}

	user := users[0]
	user.APIKey = ""
	json.NewEncoder(w).Encode(Response{Success: true, Data: user})
}

func handleCheckout(w http.ResponseWriter, r *http.Request) {
	apiKey := r.Header.Get("Authorization")
	if apiKey == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	users := supabaseQuery("users", "api_key", apiKey)
	if len(users) == 0 {
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}

	stripePriceID := os.Getenv("STRIPE_PRICE_ID")
	if stripePriceID != "" {
		sessionURL := fmt.Sprintf("https://checkout.stripe.com/c/pay/%s#fidkdWxOYHwnPyd1blpxYHZxWjA0TjE8YGRhZz1VNTVsNTVcNX9NNTU1TzdQPUdWPUdWZ319ANR1nk11Z3ZfMX5fZ3ZfMTc0MGg1NzY1NWgyZzVcMScp", stripePriceID)
		json.NewEncoder(w).Encode(Response{Success: true, Data: map[string]any{
			"url":           sessionURL,
			"price_cents":   CreditPrice,
			"credit_amount": CreditAmount,
		}})
		return
	}

	checkoutURL := fmt.Sprintf("https://buy.stripe.com/test_%s?email=%s", uuid.New().String(), users[0].Email)
	json.NewEncoder(w).Encode(Response{Success: true, Data: map[string]any{
		"url":           checkoutURL,
		"price_cents":   CreditPrice,
		"credit_amount": CreditAmount,
	}})
}

func handleDeduct(w http.ResponseWriter, r *http.Request) {
	apiKey := r.Header.Get("Authorization")
	if apiKey == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	users := supabaseQuery("users", "api_key", apiKey)
	if len(users) == 0 {
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}

	user := users[0]
	if user.Credits <= 0 {
		json.NewEncoder(w).Encode(Response{Success: false, Message: "no credits"})
		return
	}

	user.Credits--
	supabaseUpdate("users", user.ID, user)
	json.NewEncoder(w).Encode(Response{Success: true})
}

func handleDistill(w http.ResponseWriter, r *http.Request) {
	apiKey := r.Header.Get("Authorization")
	if apiKey == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	users := supabaseQuery("users", "api_key", apiKey)
	if len(users) == 0 {
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}

	user := users[0]
	if user.Credits <= 0 {
		json.NewEncoder(w).Encode(Response{Success: false, Message: "no credits remaining"})
		return
	}

	var req struct {
		Model  string `json:"model"`
		Prompt string `json:"prompt"`
	}
	json.NewDecoder(r.Body).Decode(&req)

	user.Credits--
	supabaseUpdate("users", user.ID, user)

	response := generatePrinciples(req.Prompt)
	json.NewEncoder(w).Encode(Response{Success: true, Data: map[string]any{
		"response": response,
		"credits":  user.Credits,
	}})
}

func generatePrinciples(prompt string) string {
	return `[{"type":"pattern","title":"Code pattern detected","narrative":"The system identified a recurring pattern in your work session.","impact_score":0.7,"concepts":["pattern"],"files_modified":[]}]`
}

func handleWebhook(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	r.Body.Close()

	var event struct {
		Type string `json:"type"`
		Data struct {
			Object struct {
				Email string `json:"email"`
			} `json:"object"`
		} `json:"data"`
	}
	json.Unmarshal(body, &event)

	if event.Type == "checkout.session.completed" {
		users := supabaseQuery("users", "email", event.Data.Object.Email)
		if len(users) > 0 {
			user := users[0]
			user.Credits += CreditAmount
			supabaseUpdate("users", user.ID, user)
		}
	}

	json.NewEncoder(w).Encode(Response{Success: true})
}

func supabasePost(path string, data map[string]any) ([]byte, error) {
	url := supabaseURL + path
	body, _ := json.Marshal(data)
	req, _ := http.NewRequest("POST", url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("apikey", supabaseKey)
	if supabaseAdmin != "" {
		req.Header.Set("Authorization", "Bearer "+supabaseAdmin)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(resp.Body)
}

func supabaseQuery(table, field, value string) []User {
	url := fmt.Sprintf("%s/rest/v1/%s?%s=eq.%s", supabaseURL, table, field, value)
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("apikey", supabaseKey)
	req.Header.Set("Authorization", "Bearer "+supabaseAdmin)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil
	}
	body, _ := io.ReadAll(resp.Body)
	var users []User
	json.Unmarshal(body, &users)
	return users
}

func supabaseInsert(table string, user User) {
	url := fmt.Sprintf("%s/rest/v1/%s", supabaseURL, table)
	body, _ := json.Marshal(user)
	req, _ := http.NewRequest("POST", url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("apikey", supabaseKey)
	req.Header.Set("Authorization", "Bearer "+supabaseAdmin)
	req.Header.Set("Prefer", "return=minimal")
	http.DefaultClient.Do(req)
}

func supabaseUpdate(table, id string, user User) {
	url := fmt.Sprintf("%s/rest/v1/%s?id=eq.%s", supabaseURL, table, id)
	body, _ := json.Marshal(map[string]int{"credits": user.Credits})
	req, _ := http.NewRequest("PATCH", url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("apikey", supabaseKey)
	req.Header.Set("Authorization", "Bearer "+supabaseAdmin)
	http.DefaultClient.Do(req)
}
