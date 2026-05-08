package audit

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/andreujuanc/prr/internal/ai"
	"github.com/andreujuanc/prr/internal/config"
)

// Run with: PRR_LIVE_TESTS=1 go test ./internal/audit/ -run TestLiveClassify -v

func skipWithoutAPIKey(t *testing.T) *config.Config {
	t.Helper()
	if os.Getenv("PRR_LIVE_TESTS") != "1" {
		t.Skip("PRR_LIVE_TESTS=1 not set, skipping live API test")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Skipf("no valid config: %v", err)
	}
	return cfg
}

func newLiveClassifyAgent(t *testing.T, cfg *config.Config) *ai.Agent {
	t.Helper()
	// Use the configured fast model for classification
	fastRef, err := config.ParseModelRef(cfg.FastModel)
	if err != nil {
		t.Fatalf("invalid fast_model: %v", err)
	}
	pc := cfg.ProviderConfigFor(fastRef.Provider)

	modelConfigs, err := config.LoadModels()
	if err != nil {
		t.Fatalf("LoadModels: %v", err)
	}
	mcfg := config.GetModelConfig(modelConfigs, fastRef.ModelID)

	provider, err := ai.NewProvider(ai.ProviderConfig{
		ProviderName:    fastRef.Provider,
		ModelID:         fastRef.ModelID,
		APIKey:          pc.APIKey,
		BaseURL:         pc.BaseURL,
		MaxOutputTokens: mcfg.MaxOutputTokens,
		Temperature:     mcfg.Temperature,
		ThinkingBudget:  mcfg.ThinkingBudget.Fast,
	})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	return ai.NewAgent(provider, nil)
}

func TestLiveClassify_MixedFiles(t *testing.T) {
	cfg := skipWithoutAPIKey(t)
	client := newLiveClassifyAgent(t, cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	files := []AuditFile{
		{
			Path: "internal/auth/handler.go",
			Content: `package auth

import (
	"net/http"
	"encoding/json"
)

type LoginRequest struct {
	Email    string ` + "`json:\"email\"`" + `
	Password string ` + "`json:\"password\"`" + `
}

func HandleLogin(w http.ResponseWriter, r *http.Request) {
	var req LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	// authenticate user
	w.WriteHeader(http.StatusOK)
}
`,
		},
		{
			Path: "internal/auth/handler_test.go",
			Content: `package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleLogin(t *testing.T) {
	body := strings.NewReader(` + "`" + `{"email":"a@b.com","password":"pass"}` + "`" + `)
	req := httptest.NewRequest(http.MethodPost, "/login", body)
	w := httptest.NewRecorder()
	HandleLogin(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("got %d, want 200", w.Code)
	}
}
`,
		},
		{
			Path: "internal/db/user_repo.go",
			Content: `package db

import (
	"context"
	"database/sql"
)

type UserRepo struct {
	db *sql.DB
}

func (r *UserRepo) FindByEmail(ctx context.Context, email string) (*User, error) {
	row := r.db.QueryRowContext(ctx, "SELECT id, email FROM users WHERE email = $1", email)
	var u User
	if err := row.Scan(&u.ID, &u.Email); err != nil {
		return nil, err
	}
	return &u, nil
}
`,
		},
		{
			Path: "cmd/server/main.go",
			Content: `package main

import (
	"log"
	"net/http"
)

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/login", handleLogin)
	log.Fatal(http.ListenAndServe(":8080", mux))
}
`,
		},
	}

	result, err := ClassifyFiles(ctx, client, files, nil, func(status string) {
		t.Logf("progress: %s", status)
	})
	if err != nil {
		t.Fatalf("ClassifyFiles error: %v", err)
	}

	// Verify we got a classification for every file
	for _, f := range files {
		ft, ok := result[f.Path]
		if !ok {
			t.Errorf("missing classification for %s", f.Path)
			continue
		}
		t.Logf("%s → %s", f.Path, ft)
	}

	// Check expected classifications
	expectations := map[string]FileType{
		"internal/auth/handler.go":      FileTypeHandler,
		"internal/auth/handler_test.go": FileTypeTest,
		"internal/db/user_repo.go":      FileTypeRepository,
		"cmd/server/main.go":            FileTypeInfrastructure,
	}

	for path, want := range expectations {
		got := result[path]
		if got != want {
			t.Errorf("%s: got %q, want %q", path, got, want)
		}
	}
}

func TestLiveClassify_BusinessLogicAndClient(t *testing.T) {
	cfg := skipWithoutAPIKey(t)
	client := newLiveClassifyAgent(t, cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	files := []AuditFile{
		{
			Path: "internal/billing/invoice.go",
			Content: `package billing

import "time"

type Invoice struct {
	ID        string
	Amount    float64
	Currency  string
	CreatedAt time.Time
}

func CalculateTotal(items []LineItem, taxRate float64) float64 {
	subtotal := 0.0
	for _, item := range items {
		subtotal += item.Price * float64(item.Quantity)
	}
	return subtotal * (1 + taxRate)
}

func ApplyDiscount(total float64, discountPct float64) float64 {
	if discountPct < 0 || discountPct > 100 {
		return total
	}
	return total * (1 - discountPct/100)
}
`,
		},
		{
			Path: "internal/stripe/client.go",
			Content: `package stripe

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

type Client struct {
	apiKey     string
	httpClient *http.Client
	baseURL    string
}

func NewClient(apiKey string) *Client {
	return &Client{
		apiKey:     apiKey,
		httpClient: &http.Client{},
		baseURL:    "https://api.stripe.com/v1",
	}
}

func (c *Client) CreateCharge(ctx context.Context, amount int, currency string) (*Charge, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/charges", nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("stripe API: %w", err)
	}
	defer resp.Body.Close()
	var charge Charge
	if err := json.NewDecoder(resp.Body).Decode(&charge); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &charge, nil
}
`,
		},
		{
			Path: "internal/jobs/email_worker.go",
			Content: `package jobs

import (
	"context"
	"log"
	"time"
)

type EmailWorker struct {
	queue chan EmailJob
	done  chan struct{}
}

type EmailJob struct {
	To      string
	Subject string
	Body    string
}

func NewEmailWorker(bufferSize int) *EmailWorker {
	return &EmailWorker{
		queue: make(chan EmailJob, bufferSize),
		done:  make(chan struct{}),
	}
}

func (w *EmailWorker) Start(ctx context.Context) {
	go func() {
		defer close(w.done)
		for {
			select {
			case job := <-w.queue:
				if err := sendEmail(job); err != nil {
					log.Printf("failed to send email to %s: %v", job.To, err)
				}
			case <-ctx.Done():
				return
			}
		}
	}()
}

func (w *EmailWorker) Enqueue(job EmailJob) {
	w.queue <- job
}

func sendEmail(job EmailJob) error {
	time.Sleep(100 * time.Millisecond) // simulate
	return nil
}
`,
		},
	}

	result, err := ClassifyFiles(ctx, client, files, nil, func(status string) {
		t.Logf("progress: %s", status)
	})
	if err != nil {
		t.Fatalf("ClassifyFiles error: %v", err)
	}

	expectations := map[string]FileType{
		"internal/billing/invoice.go":   FileTypeBusinessLogic,
		"internal/stripe/client.go":     FileTypeClient,
		"internal/jobs/email_worker.go": FileTypeWorker,
	}

	for path, want := range expectations {
		got := result[path]
		t.Logf("%s → %s (want %s)", path, got, want)
		if got != want {
			t.Errorf("%s: got %q, want %q", path, got, want)
		}
	}
}

func TestLiveClassify_Unknown(t *testing.T) {
	cfg := skipWithoutAPIKey(t)
	client := newLiveClassifyAgent(t, cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	files := []AuditFile{
		{
			Path: "data/schema.sql",
			Content: `CREATE TABLE users (
    id SERIAL PRIMARY KEY,
    email VARCHAR(255) NOT NULL UNIQUE,
    created_at TIMESTAMP DEFAULT NOW()
);

CREATE INDEX idx_users_email ON users(email);
`,
		},
		{
			Path: "scripts/deploy.sh",
			Content: `#!/bin/bash
set -euo pipefail

echo "Deploying to production..."
docker build -t myapp .
docker push myapp:latest
kubectl apply -f k8s/
echo "Done."
`,
		},
	}

	result, err := ClassifyFiles(ctx, client, files, nil, func(status string) {
		t.Logf("progress: %s", status)
	})
	if err != nil {
		t.Fatalf("ClassifyFiles error: %v", err)
	}

	for _, f := range files {
		ft := result[f.Path]
		t.Logf("%s → %s", f.Path, ft)
	}

	// schema.sql is borderline — repository (DB schema) or unknown are both valid
	sqlType := result["data/schema.sql"]
	if sqlType != FileTypeUnknown && sqlType != FileTypeInfrastructure && sqlType != FileTypeRepository {
		t.Errorf("data/schema.sql: got %q, want unknown, infrastructure, or repository", sqlType)
	}

	// deploy.sh is clearly infrastructure or unknown
	shType := result["scripts/deploy.sh"]
	if shType != FileTypeUnknown && shType != FileTypeInfrastructure {
		t.Errorf("scripts/deploy.sh: got %q, want unknown or infrastructure", shType)
	}
}
