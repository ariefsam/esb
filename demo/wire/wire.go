// Package wire wires all application dependencies together.
// This is manual dependency injection — no code generation required.
package wire

import (
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"
	"os"

	"github.com/gorilla/mux"
	"github.com/joho/godotenv"

	"demo/eventstore"
	"demo/projection"
	"demo/repository"
	"demo/server"
	// esb:inject:wire-imports
)

// Env holds all configuration loaded from environment variables.
type Env struct {
	Addr      string
	ESBURL    string
	TenantID  string
	ProjectID string
	JWTIssuer string
	DBDSN     string
}

// App is the root application container.
type App struct {
	Env     *Env
	Handler http.Handler
	// esb:inject:app-fields
}

// NewApp wires all dependencies and returns a ready-to-run App.
func NewApp() (*App, error) {
	_ = godotenv.Load()

	env := &Env{
		Addr:      getenv("ADDR", ":8080"),
		ESBURL:    getenv("ESB_URL", "http://localhost:8080"),
		TenantID:  getenv("TENANT_ID", "default"),
		ProjectID: getenv("PROJECT_ID", "default"),
		JWTIssuer: getenv("JWT_ISSUER", "app"),
		DBDSN:     getenv("DB_DSN", "app.db"),
	}

	privateKey, err := loadPrivateKey("private.pem")
	if err != nil {
		return nil, fmt.Errorf("load private key: %w", err)
	}

	esClient := eventstore.New(env.ESBURL, env.TenantID, env.ProjectID, env.JWTIssuer, privateKey)
	eventRepo := repository.NewEventStoreAdapter(esClient)
	_ = eventRepo

	db, err := projection.NewProjectionDB(env.DBDSN)
	if err != nil {
		return nil, fmt.Errorf("open projection db: %w", err)
	}
	_ = db

	router := mux.NewRouter()
	server.RegisterRoutes(router)

	// esb:inject:app-init

	return &App{
		Env:     env,
		Handler: router,
		// esb:inject:app-return-fields
	}, nil
}

func loadPrivateKey(path string) (*ecdsa.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("failed to decode PEM block in %s", path)
	}
	return x509.ParseECPrivateKey(block.Bytes)
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
