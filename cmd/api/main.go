package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type QuoteItem struct {
	Description string  `json:"description"`
	Quantity    float64 `json:"quantity"`
	UnitCost    float64 `json:"unit_cost"`
}

type QuoteRequest struct {
	ClientName    string      `json:"client_name"`
	Items         []QuoteItem `json:"items"`
	BufferPercent float64     `json:"buffer_percent"`
	TaxPercent    float64     `json:"tax_percent"`
	DryRun        bool        `json:"dry_run"`
}

type QuoteResponse struct {
	ID             string      `json:"id,omitempty"`
	Status         string      `json:"status"`
	ClientName     string      `json:"client_name"`
	Items          []QuoteItem `json:"items"`
	DirectCost     float64     `json:"direct_cost"`
	BufferAmount   float64     `json:"buffer_amount"`
	Subtotal       float64     `json:"subtotal"`
	TaxAmount      float64     `json:"tax_amount"`
	Total          float64     `json:"total"`
	RequiresReview bool        `json:"requires_review"`
	Message        string      `json:"message"`
	CreatedAt      string      `json:"created_at"`
	ApprovedAt     string      `json:"approved_at,omitempty"`
}

type ErrorResponse struct {
	Error string `json:"error"`
	Code  string `json:"code"`
	Hint  string `json:"hint"`
}

var (
	mu     sync.RWMutex
	quotes = make(map[string]QuoteResponse)
	logger *log.Logger
)

const (
	agentAPIKey       = "qg_agent_test_12345"
	adminAPIKey       = "qg_admin_live_98765"
	approvalThreshold = 5000000.0
	dataFilePath      = "data/quotes.json"
)

func initLogger() {
	file, err := os.OpenFile("bitacora.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		log.Fatalf("Error abriendo bitacora.log: %v", err)
	}
	logger = log.New(file, "", log.LstdFlags|log.LUTC)
}

func auditLog(actor, action, detail string) {
	entry := fmt.Sprintf("ACTOR=%s | ACTION=%s | DETAIL=%s", actor, action, detail)
	logger.Println(entry)
	fmt.Println(entry)
}

func loadQuotesFromDisk() {
	raw, err := os.ReadFile(dataFilePath)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		auditLog("system", "LOAD_QUOTES_ERROR", err.Error())
		return
	}
	var loaded []QuoteResponse
	if err := json.Unmarshal(raw, &loaded); err != nil {
		auditLog("system", "PARSE_QUOTES_ERROR", err.Error())
		return
	}
	mu.Lock()
	for _, q := range loaded {
		if q.ID != "" {
			quotes[q.ID] = q
		}
	}
	mu.Unlock()
	auditLog("system", "LOAD_QUOTES", fmt.Sprintf("loaded=%d", len(loaded)))
}

// saveQuotesLocked asume que mu ya está bloqueado para escritura
func saveQuotesLocked() error {
	if err := os.MkdirAll(filepath.Dir(dataFilePath), 0755); err != nil {
		return err
	}
	list := make([]QuoteResponse, 0, len(quotes))
	for _, q := range quotes {
		list = append(list, q)
	}
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	tmpPath := dataFilePath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmpPath, dataFilePath)
}

func generateID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return "QT-" + hex.EncodeToString(b)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func authenticate(r *http.Request) (role string, ok bool) {
	key := r.Header.Get("X-API-Key")
	switch key {
	case agentAPIKey:
		return "ai-agent", true
	case adminAPIKey:
		return "human-admin", true
	default:
		return "", false
	}
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"service": "QuoteGuard API",
		"status":  "healthy",
		"version": "1.2.0",
		"time":    time.Now().UTC().Format(time.RFC3339),
	})
}

func openAPIHandler(w http.ResponseWriter, r *http.Request) {
	spec := map[string]any{
		"openapi": "3.1.0",
		"info": map[string]any{
			"title":       "QuoteGuard API",
			"version":     "1.2.0",
			"description": "Agent-ready financial and quote gateway with dry-run simulation, human sign-off for high-value quotes, persistence, and audit logging.",
		},
		"components": map[string]any{
			"securitySchemes": map[string]any{
				"ApiKeyAuth": map[string]any{
					"type": "apiKey",
					"in":   "header",
					"name": "X-API-Key",
				},
			},
		},
		"paths": map[string]any{
			"/health": map[string]any{
				"get": map[string]any{
					"summary": "Service health check",
					"responses": map[string]any{
						"200": map[string]any{"description": "Service is healthy"},
					},
				},
			},
			"/openapi.json": map[string]any{
				"get": map[string]any{
					"summary": "OpenAPI 3.1 contract specification",
					"responses": map[string]any{
						"200": map[string]any{"description": "OpenAPI specification"},
					},
				},
			},
			"/api/v1/quotes": map[string]any{
				"post": map[string]any{
					"summary":  "Simulate or commit a quote with governance rules",
					"security": []map[string]any{{"ApiKeyAuth": []string{}}},
					"responses": map[string]any{
						"200": map[string]any{"description": "Dry-run simulation result"},
						"201": map[string]any{"description": "Quote committed"},
						"400": map[string]any{"description": "Validation or JSON error"},
						"401": map[string]any{"description": "Invalid or missing API key"},
						"403": map[string]any{"description": "Blocked by governance policy"},
					},
				},
				"get": map[string]any{
					"summary":  "List persisted quotes",
					"security": []map[string]any{{"ApiKeyAuth": []string{}}},
					"responses": map[string]any{
						"200": map[string]any{"description": "List of quotes"},
						"401": map[string]any{"description": "Invalid or missing API key"},
					},
				},
			},
			"/api/v1/quotes/{id}/approve": map[string]any{
				"post": map[string]any{
					"summary":  "Approve a pending quote (human-admin only)",
					"security": []map[string]any{{"ApiKeyAuth": []string{}}},
					"responses": map[string]any{
						"200": map[string]any{"description": "Quote approved"},
						"401": map[string]any{"description": "Invalid or missing API key"},
						"403": map[string]any{"description": "Admin role required"},
						"404": map[string]any{"description": "Quote not found"},
					},
				},
			},
		},
	}
	writeJSON(w, http.StatusOK, spec)
}

func createQuoteHandler(w http.ResponseWriter, r *http.Request) {
	role, ok := authenticate(r)
	if !ok {
		auditLog("unknown", "AUTH_FAILED", "Invalid or missing X-API-Key")
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{
			Error: "Unauthorized",
			Code:  "INVALID_API_KEY",
			Hint:  "Provide a valid X-API-Key header (agent or admin)",
		})
		return
	}

	var req QuoteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{
			Error: "Invalid JSON body",
			Code:  "MALFORMED_JSON",
			Hint:  "Check JSON syntax and field types",
		})
		return
	}

	if req.ClientName == "" || len(req.Items) == 0 {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{
			Error: "Missing required fields",
			Code:  "VALIDATION_ERROR",
			Hint:  "client_name and at least one item in items[] are required",
		})
		return
	}

	if req.BufferPercent < 0 {
		req.BufferPercent = 10
	}

	var directCost float64
	for _, item := range req.Items {
		if item.Quantity <= 0 || item.UnitCost < 0 {
			writeJSON(w, http.StatusBadRequest, ErrorResponse{
				Error: "Invalid item values",
				Code:  "INVALID_ITEM_VALUES",
				Hint:  "quantity must be > 0 and unit_cost >= 0",
			})
			return
		}
		directCost += item.Quantity * item.UnitCost
	}

	bufferAmount := directCost * (req.BufferPercent / 100.0)
	subtotal := directCost + bufferAmount
	taxAmount := subtotal * (req.TaxPercent / 100.0)
	total := subtotal + taxAmount
	requiresReview := total > approvalThreshold

	if role == "ai-agent" && !req.DryRun && requiresReview {
		auditLog(role, "BLOCKED_HIGH_VALUE_COMMIT", fmt.Sprintf("client=%s total=%.2f", req.ClientName, total))
		writeJSON(w, http.StatusForbidden, ErrorResponse{
			Error: "Human approval required for high-value quotes",
			Code:  "POLICY_REVIEW_REQUIRED",
			Hint:  "Set dry_run=true to simulate, or escalate to a human-admin key to commit quotes above 5,000,000",
		})
		return
	}

	resp := QuoteResponse{
		ClientName:     req.ClientName,
		Items:          req.Items,
		DirectCost:     directCost,
		BufferAmount:   bufferAmount,
		Subtotal:       subtotal,
		TaxAmount:      taxAmount,
		Total:          total,
		RequiresReview: requiresReview,
		CreatedAt:      time.Now().UTC().Format(time.RFC3339),
	}

	if req.DryRun {
		resp.Status = "SIMULATED"
		resp.Message = "Simulation successful. No record was persisted."
		auditLog(role, "SIMULATE_QUOTE", fmt.Sprintf("client=%s total=%.2f", req.ClientName, total))
		writeJSON(w, http.StatusOK, resp)
		return
	}

	resp.ID = generateID()
	if requiresReview {
		resp.Status = "PENDING_HUMAN_REVIEW"
		resp.Message = "Quote saved, pending final human sign-off."
	} else {
		resp.Status = "APPROVED"
		resp.Message = "Quote calculated and committed."
	}

	mu.Lock()
	quotes[resp.ID] = resp
	err := saveQuotesLocked()
	mu.Unlock()
	if err != nil {
		auditLog(role, "PERSIST_ERROR", err.Error())
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{
			Error: "Failed to persist quote",
			Code:  "STORAGE_ERROR",
			Hint:  "Check disk permissions for data/quotes.json",
		})
		return
	}

	auditLog(role, "COMMIT_QUOTE", fmt.Sprintf("id=%s client=%s total=%.2f status=%s", resp.ID, req.ClientName, total, resp.Status))
	writeJSON(w, http.StatusCreated, resp)
}

func listQuotesHandler(w http.ResponseWriter, r *http.Request) {
	role, ok := authenticate(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{
			Error: "Unauthorized",
			Code:  "INVALID_API_KEY",
			Hint:  "Provide a valid X-API-Key header",
		})
		return
	}

	mu.RLock()
	list := make([]QuoteResponse, 0, len(quotes))
	for _, q := range quotes {
		list = append(list, q)
	}
	mu.RUnlock()

	auditLog(role, "LIST_QUOTES", fmt.Sprintf("count=%d", len(list)))
	writeJSON(w, http.StatusOK, map[string]any{
		"count":  len(list),
		"quotes": list,
	})
}

func quoteSubrouteHandler(w http.ResponseWriter, r *http.Request) {
	// Esperamos rutas con forma /api/v1/quotes/{id}/approve
	trimmed := strings.TrimPrefix(r.URL.Path, "/api/v1/quotes/")
	parts := strings.Split(strings.Trim(trimmed, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] != "approve" {
		writeJSON(w, http.StatusNotFound, ErrorResponse{
			Error: "Endpoint not found",
			Code:  "NOT_FOUND",
			Hint:  "Use POST /api/v1/quotes/{id}/approve to approve a quote",
		})
		return
	}

	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, ErrorResponse{
			Error: "Method not allowed",
			Code:  "METHOD_NOT_ALLOWED",
			Hint:  "Use POST /api/v1/quotes/{id}/approve",
		})
		return
	}

	role, ok := authenticate(r)
	if !ok {
		auditLog("unknown", "AUTH_FAILED", "Invalid or missing X-API-Key on approve")
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{
			Error: "Unauthorized",
			Code:  "INVALID_API_KEY",
			Hint:  "Provide a valid X-API-Key header",
		})
		return
	}

	quoteID := parts[0]
	if role != "human-admin" {
		auditLog(role, "APPROVE_FORBIDDEN", fmt.Sprintf("id=%s", quoteID))
		writeJSON(w, http.StatusForbidden, ErrorResponse{
			Error: "Only human-admin can approve quotes",
			Code:  "ADMIN_REQUIRED",
			Hint:  "Use a human-admin X-API-Key to approve quotes",
		})
		return
	}

	mu.Lock()
	q, exists := quotes[quoteID]
	if !exists {
		mu.Unlock()
		writeJSON(w, http.StatusNotFound, ErrorResponse{
			Error: "Quote not found",
			Code:  "QUOTE_NOT_FOUND",
			Hint:  "Verify the quote ID via GET /api/v1/quotes",
		})
		return
	}

	q.Status = "APPROVED"
	q.ApprovedAt = time.Now().UTC().Format(time.RFC3339)
	q.Message = "Quote approved by human-admin."
	quotes[quoteID] = q
	err := saveQuotesLocked()
	mu.Unlock()

	if err != nil {
		auditLog(role, "PERSIST_ERROR", err.Error())
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{
			Error: "Failed to persist approved quote",
			Code:  "STORAGE_ERROR",
			Hint:  "Check disk permissions for data/quotes.json",
		})
		return
	}

	auditLog(role, "APPROVE_QUOTE", fmt.Sprintf("id=%s total=%.2f", q.ID, q.Total))
	writeJSON(w, http.StatusOK, q)
}

func main() {
	initLogger()
	loadQuotesFromDisk()

	mux := http.NewServeMux()
	mux.HandleFunc("/health", healthHandler)
	mux.HandleFunc("/openapi.json", openAPIHandler)
	mux.HandleFunc("/api/v1/quotes", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			createQuoteHandler(w, r)
		case http.MethodGet:
			listQuotesHandler(w, r)
		default:
			writeJSON(w, http.StatusMethodNotAllowed, ErrorResponse{
				Error: "Method not allowed",
				Code:  "METHOD_NOT_ALLOWED",
				Hint:  "Supported methods: GET, POST",
			})
		}
	})
	mux.HandleFunc("/api/v1/quotes/", quoteSubrouteHandler)

	addr := ":8080"
	auditLog("system", "SERVER_START", "QuoteGuard API listening on "+addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}
