package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/justar9/btick/internal/domain"
)

var errAPIAccountExists = errors.New("api account already exists")

type signupRequest struct {
	Email string `json:"email"`
	Name  string `json:"name,omitempty"`
}

type apiAccountResponse struct {
	AccountID    string  `json:"account_id"`
	Email        string  `json:"email"`
	Name         string  `json:"name,omitempty"`
	Tier         string  `json:"tier"`
	APIKeyPrefix string  `json:"api_key_prefix"`
	Active       bool    `json:"active"`
	CreatedAt    string  `json:"created_at"`
	LastUsedAt   *string `json:"last_used_at,omitempty"`
}

type authContextKey string

const apiAccountContextKey authContextKey = "api-account"

func (s *Server) handleSignup(w http.ResponseWriter, r *http.Request) {
	if !s.accessCfg.Enabled || !s.accessCfg.SignupEnabled {
		http.NotFound(w, r)
		return
	}

	if s.store() == nil {
		http.Error(w, `{"error":"database not available"}`, http.StatusServiceUnavailable)
		return
	}

	var req signupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid json body"}`, http.StatusBadRequest)
		return
	}
	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	req.Name = strings.TrimSpace(req.Name)
	if !strings.Contains(req.Email, "@") {
		http.Error(w, `{"error":"valid email required"}`, http.StatusBadRequest)
		return
	}

	apiKey, prefix, hash, err := generateAPIKey()
	if err != nil {
		s.logger.Error("generate api key failed", "error", err)
		http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
		return
	}

	account, err := s.store().CreateAPIAccount(r.Context(), req.Email, req.Name, s.accessCfg.SignupTier(), hash, prefix)
	if err != nil {
		if errors.Is(err, errAPIAccountExists) || strings.Contains(strings.ToLower(err.Error()), "api account already exists") || strings.Contains(strings.ToLower(err.Error()), "duplicate") {
			http.Error(w, `{"error":"account already exists"}`, http.StatusConflict)
			return
		}
		s.logger.Error("create api account failed", "error", err)
		http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	s.writeJSON(w, map[string]any{
		"account_id":     account.AccountID.String(),
		"email":          account.Email,
		"name":           account.Name,
		"tier":           account.Tier,
		"api_key":        apiKey,
		"api_key_prefix": account.APIKeyPrefix,
		"created_at":     account.CreatedAt.Format(time.RFC3339Nano),
	})
}

func (s *Server) handleAuthMe(w http.ResponseWriter, r *http.Request) {
	account := apiAccountFromContext(r.Context())
	if account == nil {
		http.Error(w, `{"error":"api key required"}`, http.StatusUnauthorized)
		return
	}

	s.writeJSON(w, newAPIAccountResponse(account))
}

func (s *Server) requireTier(requiredTier string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.accessCfg.Enabled {
			next.ServeHTTP(w, r)
			return
		}

		account, status, message := s.authenticateRequest(r, requiredTier)
		if status != 0 {
			http.Error(w, message, status)
			return
		}

		if account != nil {
			w.Header().Set("X-API-Tier", account.Tier)
			ctx := context.WithValue(r.Context(), apiAccountContextKey, account)
			r = r.WithContext(ctx)
		}

		next.ServeHTTP(w, r)
	})
}

func (s *Server) authenticateRequest(r *http.Request, requiredTier string) (*domain.APIAccount, int, string) {
	apiKey := extractAPIKey(r)
	if apiKey == "" {
		return nil, http.StatusUnauthorized, `{"error":"api key required"}`
	}
	store := s.store()
	if store == nil {
		return nil, http.StatusServiceUnavailable, `{"error":"database not available"}`
	}

	account, err := store.LookupAPIAccountByKeyHash(r.Context(), hashAPIKey(apiKey))
	if err != nil || account == nil || !account.Active {
		return nil, http.StatusUnauthorized, `{"error":"invalid api key"}`
	}
	if tierRank(account.Tier) < tierRank(requiredTier) {
		return nil, http.StatusForbidden, `{"error":"tier upgrade required"}`
	}

	now := time.Now().UTC()
	_ = store.TouchAPIAccountLastUsed(r.Context(), account.AccountID.String(), now)
	account.LastUsedAt = now
	return account, 0, ""
}

func newAPIAccountResponse(account *domain.APIAccount) apiAccountResponse {
	resp := apiAccountResponse{
		AccountID:    account.AccountID.String(),
		Email:        account.Email,
		Name:         account.Name,
		Tier:         account.Tier,
		APIKeyPrefix: account.APIKeyPrefix,
		Active:       account.Active,
		CreatedAt:    account.CreatedAt.Format(time.RFC3339Nano),
	}
	if !account.LastUsedAt.IsZero() {
		lastUsedAt := account.LastUsedAt.Format(time.RFC3339Nano)
		resp.LastUsedAt = &lastUsedAt
	}
	return resp
}

func extractAPIKey(r *http.Request) string {
	if header := strings.TrimSpace(r.Header.Get("X-API-Key")); header != "" {
		return header
	}
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		return strings.TrimSpace(auth[7:])
	}
	if r.URL.Query().Has("api_key") {
		return strings.TrimSpace(r.URL.Query().Get("api_key"))
	}
	return ""
}

func generateAPIKey() (raw, prefix, hash string, err error) {
	buf := make([]byte, 24)
	if _, err = rand.Read(buf); err != nil {
		return "", "", "", err
	}
	secret := hex.EncodeToString(buf)
	raw = "btk_" + secret
	if len(raw) > 12 {
		prefix = raw[:12]
	} else {
		prefix = raw
	}
	hash = hashAPIKey(raw)
	return raw, prefix, hash, nil
}

func hashAPIKey(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func tierRank(tier string) int {
	switch strings.ToLower(strings.TrimSpace(tier)) {
	case "pro":
		return 2
	case "starter":
		return 1
	default:
		return 0
	}
}

func apiAccountFromContext(ctx context.Context) *domain.APIAccount {
	account, _ := ctx.Value(apiAccountContextKey).(*domain.APIAccount)
	return account
}

func newAPIAccount(email, name, tier, prefix string) *domain.APIAccount {
	return &domain.APIAccount{
		AccountID:    uuid.New(),
		Email:        email,
		Name:         name,
		Tier:         tier,
		APIKeyPrefix: prefix,
		Active:       true,
		CreatedAt:    time.Now().UTC(),
	}
}
