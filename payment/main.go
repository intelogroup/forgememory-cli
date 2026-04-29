package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// --- In-memory fallback store (used when Supabase is unavailable / no service key) ---

var (
	memMu           sync.RWMutex
	memByAPIKey     = map[string]User{}
	memByEmail      = map[string]string{} // email → api_key
	memRuns         []map[string]any
)

func memGetByField(field, value string) []User {
	memMu.RLock()
	defer memMu.RUnlock()
	switch field {
	case "api_key":
		if u, ok := memByAPIKey[value]; ok {
			return []User{u}
		}
	case "email":
		if key, ok := memByEmail[value]; ok {
			if u, ok2 := memByAPIKey[key]; ok2 {
				return []User{u}
			}
		}
	case "id":
		for _, u := range memByAPIKey {
			if u.ID == value {
				return []User{u}
			}
		}
	}
	return nil
}

func memInsertUser(u User) {
	memMu.Lock()
	defer memMu.Unlock()
	memByAPIKey[u.APIKey] = u
	memByEmail[u.Email] = u.APIKey
}

func memUpdateCredits(id string, credits int) {
	memMu.Lock()
	defer memMu.Unlock()
	for key, u := range memByAPIKey {
		if u.ID == id {
			u.Credits = credits
			memByAPIKey[key] = u
			break
		}
	}
}

func memInsertRun(data map[string]any) {
	memMu.Lock()
	defer memMu.Unlock()
	memRuns = append(memRuns, data)
}

func memGetRuns(userID string) []map[string]any {
	memMu.RLock()
	defer memMu.RUnlock()
	var out []map[string]any
	for i := len(memRuns) - 1; i >= 0; i-- {
		r := memRuns[i]
		if uid, ok := r["user_id"].(string); ok && uid == userID {
			out = append(out, r)
		}
	}
	return out
}

var (
	supabaseURL   = os.Getenv("SUPABASE_URL")
	supabaseKey   = os.Getenv("SUPABASE_ANON_KEY")
	supabaseAdmin = os.Getenv("SUPABASE_SERVICE_KEY")
	stripeKey     = os.Getenv("STRIPE_SECRET_KEY")
	stripeWebhook = os.Getenv("STRIPE_WEBHOOK_SECRET")
	resendKey     = os.Getenv("RESEND_API_KEY")
)

// LLM configuration resolved at startup.
var (
	llmAPIKey    string
	llmBaseURL   string
	llmModel     string
	llmCostInput  float64
	llmCostOutput float64
)

func init() {
	// Resolve LLM API key: LLM_API_KEY > GROQ_API_KEY > OPENAI_API_KEY
	llmAPIKey = os.Getenv("LLM_API_KEY")
	if llmAPIKey == "" {
		llmAPIKey = os.Getenv("GROQ_API_KEY")
	}
	if llmAPIKey == "" {
		llmAPIKey = os.Getenv("OPENAI_API_KEY")
	}

	llmBaseURL = os.Getenv("LLM_BASE_URL")
	if llmBaseURL == "" {
		llmBaseURL = "https://api.groq.com/openai"
	}

	llmModel = os.Getenv("LLM_MODEL")
	if llmModel == "" {
		llmModel = "llama-3.1-8b-instant"
	}

	// Groq llama-3.1-8b-instant pricing: $0.05/MTok input, $0.08/MTok output
	llmCostInput = parseFloatEnv("LLM_COST_INPUT", 0.00000005)
	llmCostOutput = parseFloatEnv("LLM_COST_OUTPUT", 0.00000008)
}

func parseFloatEnv(key string, def float64) float64 {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return def
	}
	return f
}

const (
	InitialCredits = 5
	// 1 credit = $0.005
	creditsPerDollar = 200
)

// packCredits maps pack_id to number of credits awarded.
var packCredits = map[string]int{
	"pack_5":  1000,
	"pack_20": 4200,
	"pack_50": 11000,
}

// packPriceCents maps pack_id to price in cents for Stripe.
var packPriceCents = map[string]int{
	"pack_5":  500,
	"pack_20": 2000,
	"pack_50": 5000,
}

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

// --- Per-user rate limiter (token bucket, 10 inferences/minute) ---

type rateBucket struct {
	mu       sync.Mutex
	tokens   int
	lastFill time.Time
}

func (b *rateBucket) allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	const cap = 10
	refill := int(time.Since(b.lastFill).Minutes() * cap)
	if refill > 0 {
		b.tokens = min(b.tokens+refill, cap)
		b.lastFill = time.Now()
	}
	if b.tokens <= 0 {
		return false
	}
	b.tokens--
	return true
}

var inferenceRateLimiter sync.Map // api_key → *rateBucket

func inferenceAllowed(apiKey string) bool {
	val, _ := inferenceRateLimiter.LoadOrStore(apiKey, &rateBucket{tokens: 10, lastFill: time.Now()})
	return val.(*rateBucket).allow()
}

func main() {
	if supabaseURL == "" || supabaseKey == "" {
		log.Fatal("SUPABASE_URL and SUPABASE_ANON_KEY required")
	}

	// Web app endpoints (v1 prefix)
	http.HandleFunc("/webapp-auth/send-link", handleSendLink)
	http.HandleFunc("/v1/stats", handleStats)
	http.HandleFunc("/v1/activity", handleActivity)
	http.HandleFunc("/v1/user/settings", handleUserSettings)
	http.HandleFunc("/v1/user/api-key", handleUserAPIKey)
	http.HandleFunc("/v1/sync/pull", handleSyncPull)
	http.HandleFunc("/v1/checkout", handleCheckout)
	http.HandleFunc("/v1/inference", handleDistill)
	http.HandleFunc("/api/v1/inference", handleDistill) // alias for old CLI compat

	// Health check
	http.HandleFunc("/health", handleHealth)

	// Stripe webhook (keep /api/webhook path)
	http.HandleFunc("/api/webhook", handleWebhook)

	// Legacy CLI aliases (backward compat)
	http.HandleFunc("/api/login", handleLogin)
	http.HandleFunc("/api/register", handleRegister)
	http.HandleFunc("/api/credits", handleCredits)
	http.HandleFunc("/api/whoami", handleWhoami)
	http.HandleFunc("/api/deduct", handleDeduct)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8000"
	}
	log.Printf("Payment service starting on :%s (LLM: %s, model: %s)", port, llmBaseURL, llmModel)
	log.Fatal(http.ListenAndServe(":"+port, corsMiddleware(http.DefaultServeMux)))
}

// corsMiddleware adds CORS headers so the Next.js dev server can call this backend.
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// --- Auth helpers ---

// bearerToken extracts the token from "Authorization: Bearer <token>" or plain "Authorization: <token>".
func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if h == "" {
		return ""
	}
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	return h
}

// requireUser looks up a user by api_key from the Authorization header.
// Returns (user, true) on success, writes 401 and returns (User{}, false) on failure.
func requireUser(w http.ResponseWriter, r *http.Request) (User, bool) {
	token := bearerToken(r)
	if token == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return User{}, false
	}
	users := supabaseQuery("users", "api_key", token)
	if len(users) == 0 {
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return User{}, false
	}
	return users[0], true
}

// --- Handlers ---

func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(Response{Success: true})
}

// POST /webapp-auth/send-link
// body: {email, callback_url}
// Looks up or creates user, sends magic link email.
func handleSendLink(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Email       string `json:"email"`
		CallbackURL string `json:"callback_url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Email == "" {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// Look up existing user or create one.
	users := supabaseQuery("users", "email", req.Email)
	var user User
	if len(users) > 0 {
		user = users[0]
	} else {
		user = User{
			ID:        uuid.New().String(),
			Email:     req.Email,
			Credits:   InitialCredits,
			APIKey:    uuid.New().String(),
			CreatedAt: time.Now().UTC().Format(time.RFC3339),
		}
		if err := supabaseInsert("users", user); err != nil {
			log.Printf("failed to create user: %v", err)
			http.Error(w, "failed to create user", http.StatusInternalServerError)
			return
		}
	}

	// Build magic link.
	magicLink := req.CallbackURL
	if magicLink == "" {
		magicLink = "http://localhost:3000/auth/callback"
	}
	if !strings.Contains(magicLink, "?") {
		magicLink += "?token=" + user.APIKey
	} else {
		magicLink += "&token=" + user.APIKey
	}

	emailSent := false
	if resendKey != "" {
		if err := sendMagicLinkEmail(req.Email, magicLink); err != nil {
			log.Printf("failed to send email: %v", err)
		} else {
			emailSent = true
		}
	}
	if !emailSent {
		log.Printf("[DEV] Magic link for %s: %s", req.Email, magicLink)
	}

	w.Header().Set("Content-Type", "application/json")
	// Include magic_link in dev (no verified email domain) so tests can use it directly.
	resp := map[string]any{"success": true, "message": "link sent"}
	if !emailSent {
		resp["magic_link"] = magicLink
	}
	json.NewEncoder(w).Encode(resp)
}

// sendMagicLinkEmail sends a magic link via Resend.
func sendMagicLinkEmail(to, link string) error {
	body := map[string]any{
		"from":    "ForgeMemo <noreply@forgememo.com>",
		"to":      []string{to},
		"subject": "Sign in to ForgeMemo",
		"html":    fmt.Sprintf(`<p>Click the link below to sign in:</p><p><a href="%s">Sign in</a></p><p>Or copy this link: %s</p>`, link, link),
	}
	bodyBytes, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", "https://api.resend.com/emails", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+resendKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("resend returned %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

// GET /v1/stats
func handleStats(w http.ResponseWriter, r *http.Request) {
	user, ok := requireUser(w, r)
	if !ok {
		return
	}

	balanceUSD := float64(user.Credits) * 0.005

	// Count total runs from the runs table.
	totalRuns := supabaseCountRuns(user.ID)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"total_runs":  totalRuns,
		"balance_usd": balanceUSD,
		"traces":      nil,
		"principles":  nil,
		"projects":    nil,
	})
}

// GET /v1/activity
// Returns Run[] where Run = {model, cost_usd, ts}
func handleActivity(w http.ResponseWriter, r *http.Request) {
	user, ok := requireUser(w, r)
	if !ok {
		return
	}

	runs := supabaseQueryRuns(user.ID)

	w.Header().Set("Content-Type", "application/json")
	if runs == nil {
		runs = []map[string]any{}
	}
	json.NewEncoder(w).Encode(runs)
}

// GET /v1/user/settings
func handleUserSettings(w http.ResponseWriter, r *http.Request) {
	_, ok := requireUser(w, r)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"provider": "forgememo",
	})
}

// GET /v1/user/api-key
func handleUserAPIKey(w http.ResponseWriter, r *http.Request) {
	user, ok := requireUser(w, r)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"api_key": user.APIKey,
	})
}

// GET /v1/sync/pull
func handleSyncPull(w http.ResponseWriter, r *http.Request) {
	_, ok := requireUser(w, r)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode([]any{})
}

// POST /v1/checkout
// body: {pack_id, success_url}
func handleCheckout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	user, ok := requireUser(w, r)
	if !ok {
		return
	}

	var req struct {
		PackID     string `json:"pack_id"`
		SuccessURL string `json:"success_url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	credits, validPack := packCredits[req.PackID]
	if !validPack {
		http.Error(w, "invalid pack_id", http.StatusBadRequest)
		return
	}
	priceCents := packPriceCents[req.PackID]

	successURL := req.SuccessURL
	if successURL == "" {
		successURL = "http://localhost:3000/dashboard?checkout=success"
	}

	if stripeKey == "" {
		// Dev mode: return a fake checkout URL.
		testURL := fmt.Sprintf("http://localhost:3000/checkout/test?pack_id=%s&credits=%d", req.PackID, credits)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"url": testURL})
		return
	}

	sessionURL, err := createStripeCheckoutSession(user, req.PackID, priceCents, credits, successURL)
	if err != nil {
		log.Printf("stripe checkout error: %v", err)
		http.Error(w, "failed to create checkout session", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"url": sessionURL})
}

// createStripeCheckoutSession creates a Stripe checkout.session via REST API.
func createStripeCheckoutSession(user User, packID string, priceCents, credits int, successURL string) (string, error) {
	params := url.Values{}
	params.Set("payment_method_types[]", "card")
	params.Set("line_items[0][price_data][currency]", "usd")
	params.Set("line_items[0][price_data][unit_amount]", strconv.Itoa(priceCents))
	params.Set("line_items[0][price_data][product_data][name]", fmt.Sprintf("ForgeMemo Credits (%d)", credits))
	params.Set("line_items[0][quantity]", "1")
	params.Set("mode", "payment")
	params.Set("success_url", successURL)
	params.Set("cancel_url", "http://localhost:3000/billing")
	params.Set("customer_email", user.Email)
	params.Set("metadata[pack_id]", packID)
	params.Set("metadata[user_id]", user.ID)

	req, _ := http.NewRequest("POST", "https://api.stripe.com/v1/checkout/sessions", strings.NewReader(params.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(stripeKey, "")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("stripe returned %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(body, &result); err != nil || result.URL == "" {
		return "", fmt.Errorf("unexpected stripe response: %s", string(body))
	}
	return result.URL, nil
}

// POST /v1/inference (also /api/v1/inference for old CLI compat)
func handleDistill(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	user, ok := requireUser(w, r)
	if !ok {
		return
	}

	if !inferenceAllowed(user.APIKey) {
		w.Header().Set("Retry-After", "60")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		json.NewEncoder(w).Encode(Response{Success: false, Message: "rate limit exceeded: max 10 inferences per minute"})
		return
	}

	var req struct {
		Model  string `json:"model"`
		Prompt string `json:"prompt"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// Atomically deduct one credit via stored procedure (single UPDATE, no TOCTOU).
	var deducted bool
	if supabaseURL != "" && supabaseAdmin != "" {
		if err := supabaseRPC("deduct_credit", map[string]any{"user_api_key": user.APIKey}, &deducted); err != nil {
			log.Printf("deduct_credit rpc error: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	} else {
		// Dev fallback (in-memory)
		memMu.Lock()
		u, ok2 := memByAPIKey[user.APIKey]
		if ok2 && u.Credits > 0 {
			u.Credits--
			memByAPIKey[user.APIKey] = u
			deducted = true
		}
		memMu.Unlock()
	}
	if !deducted {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusPaymentRequired)
		json.NewEncoder(w).Encode(Response{Success: false, Message: "no credits remaining"})
		return
	}

	response, tokensIn, tokensOut := generatePrinciples(req.Prompt)

	// Track cost and insert run record.
	costUSD := float64(tokensIn)*llmCostInput + float64(tokensOut)*llmCostOutput
	runData := map[string]any{
		"user_id":    user.ID,
		"model":      llmModel,
		"tokens_in":  tokensIn,
		"tokens_out": tokensOut,
		"cost_usd":   costUSD,
		"ts":         time.Now().UTC().Format(time.RFC3339),
	}
	if err := supabaseInsertMap("runs", runData); err != nil {
		log.Printf("failed to insert run record: %v", err)
		// Non-fatal; continue.
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(Response{Success: true, Data: map[string]any{
		"response": response,
		"credits":  user.Credits,
	}})
}

// generatePrinciples calls the configured LLM backend.
// It always uses the server's LLM_MODEL, ignoring any client-supplied model name.
func generatePrinciples(prompt string) (response string, tokensIn, tokensOut int) {
	if llmAPIKey == "" {
		return `[{"type":"pattern","title":"Code pattern detected (Mock)","narrative":"The system identified a recurring pattern in your work session. (LLM key not configured on server)","impact_score":0.7,"concepts":["pattern"],"files_modified":[]}]`, 0, 0
	}

	endpoint := strings.TrimRight(llmBaseURL, "/") + "/v1/chat/completions"

	body := map[string]any{
		"model": llmModel,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
	}
	jsonBody, _ := json.Marshal(body)

	req, _ := http.NewRequest("POST", endpoint, bytes.NewReader(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+llmAPIKey)

	client := &http.Client{Timeout: 90 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Sprintf(`[{"type":"pattern","title":"LLM Error","narrative":"Failed to call LLM: %v","impact_score":0.1,"concepts":["gotcha"],"files_modified":[]}]`, err), 0, 0
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 400 {
		log.Printf("LLM API error %d: %s", resp.StatusCode, string(data))
		return fmt.Sprintf(`[{"type":"pattern","title":"LLM API Error","narrative":"LLM returned status %d","impact_score":0.1,"concepts":["gotcha"],"files_modified":[]}]`, resp.StatusCode), 0, 0
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	json.Unmarshal(data, &result)

	tokensIn = result.Usage.PromptTokens
	tokensOut = result.Usage.CompletionTokens

	if len(result.Choices) > 0 {
		return result.Choices[0].Message.Content, tokensIn, tokensOut
	}
	return `[]`, tokensIn, tokensOut
}

// POST /api/webhook — Stripe webhook
func handleWebhook(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	r.Body.Close()

	if stripeWebhook != "" && !verifyStripeSignature(r.Header.Get("Stripe-Signature"), body, stripeWebhook) {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	var event struct {
		ID   string `json:"id"`
		Type string `json:"type"`
		Data struct {
			Object struct {
				Email    string `json:"customer_email"`
				Metadata struct {
					PackID string `json:"pack_id"`
					UserID string `json:"user_id"`
				} `json:"metadata"`
			} `json:"object"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &event); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}

	if event.Type == "checkout.session.completed" {
		meta := event.Data.Object.Metadata
		credits, validPack := packCredits[meta.PackID]
		if !validPack {
			log.Printf("webhook: unknown pack_id %q, skipping credits", meta.PackID)
			// ACK to Stripe — don't retry forever for bad metadata
		} else {
			var users []User
			if meta.UserID != "" {
				users = supabaseQuery("users", "id", meta.UserID)
			} else if event.Data.Object.Email != "" {
				users = supabaseQuery("users", "email", event.Data.Object.Email)
			}

			if len(users) > 0 {
				var applied bool
				if err := supabaseRPC("add_credits_idempotent", map[string]any{
					"p_event_id": event.ID,
					"p_user_id":  users[0].ID,
					"p_amount":   credits,
				}, &applied); err != nil {
					log.Printf("webhook: rpc error: %v", err)
					http.Error(w, "failed to apply credits", http.StatusInternalServerError)
					return
				}
				if applied {
					log.Printf("webhook: added %d credits to user %s (event: %s)", credits, users[0].ID, event.ID)
				} else {
					log.Printf("webhook: duplicate event %s, skipping", event.ID)
				}
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(Response{Success: true})
}

// --- Legacy CLI handlers ---

func handleRegister(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

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
	if err := supabaseInsert("users", user); err != nil {
		json.NewEncoder(w).Encode(Response{Success: false, Message: err.Error()})
		return
	}

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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

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
	user, ok := requireUser(w, r)
	if !ok {
		return
	}
	json.NewEncoder(w).Encode(Response{Success: true, Data: map[string]int{"credits": user.Credits}})
}

func handleWhoami(w http.ResponseWriter, r *http.Request) {
	user, ok := requireUser(w, r)
	if !ok {
		return
	}
	user.APIKey = ""
	json.NewEncoder(w).Encode(Response{Success: true, Data: user})
}

func handleDeduct(w http.ResponseWriter, r *http.Request) {
	user, ok := requireUser(w, r)
	if !ok {
		return
	}

	if user.Credits <= 0 {
		json.NewEncoder(w).Encode(Response{Success: false, Message: "no credits"})
		return
	}

	user.Credits--
	if err := supabaseUpdate("users", user.ID, user); err != nil {
		json.NewEncoder(w).Encode(Response{Success: false, Message: err.Error()})
		return
	}
	json.NewEncoder(w).Encode(Response{Success: true})
}

// --- Supabase helpers ---

func supabasePost(path string, data map[string]any) ([]byte, error) {
	reqURL := supabaseURL + path
	body, _ := json.Marshal(data)
	req, _ := http.NewRequest("POST", reqURL, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("apikey", supabaseKey)
	if supabaseAdmin != "" {
		req.Header.Set("Authorization", "Bearer "+supabaseAdmin)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("supabase POST %s failed with status %d", path, resp.StatusCode)
	}
	return respBody, nil
}

func supabaseQuery(table, field, value string) []User {
	if supabaseURL != "" && supabaseAdmin != "" {
		reqURL := fmt.Sprintf("%s/rest/v1/%s?%s=eq.%s", supabaseURL, table, field, url.QueryEscape(value))
		req, _ := http.NewRequest("GET", reqURL, nil)
		req.Header.Set("apikey", supabaseKey)
		req.Header.Set("Authorization", "Bearer "+supabaseAdmin)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			defer resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				body, _ := io.ReadAll(resp.Body)
				var users []User
				if json.Unmarshal(body, &users) == nil && len(users) > 0 {
					return users
				}
			}
		}
	}
	// Fallback: in-memory store
	return memGetByField(field, value)
}

func supabaseInsert(table string, user User) error {
	if supabaseURL != "" && supabaseAdmin != "" {
		reqURL := fmt.Sprintf("%s/rest/v1/%s", supabaseURL, table)
		body, _ := json.Marshal(user)
		req, _ := http.NewRequest("POST", reqURL, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("apikey", supabaseKey)
		req.Header.Set("Authorization", "Bearer "+supabaseAdmin)
		req.Header.Set("Prefer", "return=minimal")
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			defer resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return nil
			}
			b, _ := io.ReadAll(resp.Body)
			log.Printf("supabase insert failed (%d): %s — using in-memory fallback", resp.StatusCode, string(b))
		}
	}
	// Fallback: store in memory
	memInsertUser(user)
	return nil
}

// supabaseInsertMap inserts an arbitrary map into a Supabase table.
func supabaseInsertMap(table string, data map[string]any) error {
	if supabaseURL != "" && supabaseAdmin != "" {
		reqURL := fmt.Sprintf("%s/rest/v1/%s", supabaseURL, table)
		body, _ := json.Marshal(data)
		req, _ := http.NewRequest("POST", reqURL, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("apikey", supabaseKey)
		req.Header.Set("Authorization", "Bearer "+supabaseAdmin)
		req.Header.Set("Prefer", "return=minimal")
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			defer resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return nil
			}
		}
	}
	if table == "runs" {
		memInsertRun(data)
	}
	return nil
}


func supabaseUpdate(table, id string, user User) error {
	if supabaseURL != "" && supabaseAdmin != "" {
		reqURL := fmt.Sprintf("%s/rest/v1/%s?id=eq.%s", supabaseURL, table, id)
		body, _ := json.Marshal(map[string]int{"credits": user.Credits})
		req, _ := http.NewRequest("PATCH", reqURL, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("apikey", supabaseKey)
		req.Header.Set("Authorization", "Bearer "+supabaseAdmin)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			defer resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return nil
			}
		}
	}
	// Fallback: update in memory
	memUpdateCredits(id, user.Credits)
	return nil
}

// supabaseRPC calls a Supabase stored function via the PostgREST RPC endpoint.
func supabaseRPC(funcName string, args map[string]any, dest any) error {
	reqURL := fmt.Sprintf("%s/rest/v1/rpc/%s", supabaseURL, funcName)
	body, _ := json.Marshal(args)
	req, _ := http.NewRequest("POST", reqURL, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("apikey", supabaseKey)
	req.Header.Set("Authorization", "Bearer "+supabaseAdmin)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("rpc %s status %d: %s", funcName, resp.StatusCode, string(respBody))
	}
	if dest != nil {
		return json.Unmarshal(respBody, dest)
	}
	return nil
}

// supabaseQueryRuns returns recent runs for a user as raw maps.
func supabaseQueryRuns(userID string) []map[string]any {
	if supabaseURL != "" && supabaseAdmin != "" {
		reqURL := fmt.Sprintf("%s/rest/v1/runs?user_id=eq.%s&order=ts.desc&limit=100",
			supabaseURL, url.QueryEscape(userID))
		req, _ := http.NewRequest("GET", reqURL, nil)
		req.Header.Set("apikey", supabaseKey)
		req.Header.Set("Authorization", "Bearer "+supabaseAdmin)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			defer resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				body, _ := io.ReadAll(resp.Body)
				var runs []map[string]any
				if json.Unmarshal(body, &runs) == nil {
					return runs
				}
			}
		}
	}
	return memGetRuns(userID)
}

// supabaseCountRuns returns the count of runs for a user.
func supabaseCountRuns(userID string) int {
	if supabaseURL != "" && supabaseAdmin != "" {
		reqURL := fmt.Sprintf("%s/rest/v1/runs?user_id=eq.%s&select=id",
			supabaseURL, url.QueryEscape(userID))
		req, _ := http.NewRequest("GET", reqURL, nil)
		req.Header.Set("apikey", supabaseKey)
		req.Header.Set("Authorization", "Bearer "+supabaseAdmin)
		req.Header.Set("Prefer", "count=exact")
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			defer resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				if cr := resp.Header.Get("Content-Range"); cr != "" {
					parts := strings.SplitN(cr, "/", 2)
					if len(parts) == 2 {
						if n, err := strconv.Atoi(parts[1]); err == nil {
							return n
						}
					}
				}
				body, _ := io.ReadAll(resp.Body)
				var rows []map[string]any
				json.Unmarshal(body, &rows)
				return len(rows)
			}
		}
	}
	// Fallback: count from in-memory runs.
	return len(memGetRuns(userID))
}

func verifyStripeSignature(sigHeader string, body []byte, secret string) bool {
	if secret == "" {
		return true
	}

	var ts string
	var sigs []string
	for _, part := range strings.Split(sigHeader, ",") {
		p := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(p) != 2 {
			continue
		}
		switch p[0] {
		case "t":
			ts = p[1]
		case "v1":
			sigs = append(sigs, p[1])
		}
	}
	if ts == "" || len(sigs) == 0 {
		return false
	}

	parsedTS, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return false
	}
	now := time.Now().Unix()
	if parsedTS < now-300 || parsedTS > now+300 {
		return false
	}

	payload := []byte(fmt.Sprintf("%s.%s", ts, string(body)))
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	expected := mac.Sum(nil)

	for _, sig := range sigs {
		decoded, err := hex.DecodeString(sig)
		if err != nil {
			continue
		}
		if hmac.Equal(decoded, expected) {
			return true
		}
	}
	return false
}
