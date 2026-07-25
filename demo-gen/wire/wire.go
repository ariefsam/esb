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

	"/tmp/demo-gen/domain"
	"/tmp/demo-gen/eventstore"
	"/tmp/demo-gen/projection"
	"/tmp/demo-gen/repository"
	"/tmp/demo-gen/server"
	// esb:inject:wire-imports
)

// Env holds all configuration loaded from environment variables.
type Env struct {
	Addr           string
	ESBURL         string
	TenantID       string
	ProjectID      string
	JWTIssuer      string
	DBDSN          string
	EventStoreMode string // "embedded" or "esb-server"
	EventStoreDSN  string // optional override; defaults to DBDSN when embedded
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
		Addr:           getenv("ADDR", ":8080"),
		ESBURL:         getenv("ESB_URL", "http://localhost:8080"),
		TenantID:       getenv("TENANT_ID", "default"),
		ProjectID:      getenv("PROJECT_ID", "default"),
		JWTIssuer:      getenv("JWT_ISSUER", "app"),
		DBDSN:          getenv("DB_DSN", "app.db"),
		EventStoreMode: getenv("EVENT_STORE_MODE", "embedded"),
		EventStoreDSN:  getenv("EVENT_STORE_DSN", ""),
	}
	if env.EventStoreMode == "embedded" && env.EventStoreDSN == "" {
		env.EventStoreDSN = env.DBDSN
	}

	esClient := eventstore.New(env.ESBURL, env.TenantID, env.ProjectID, env.JWTIssuer, mustLoadKey())
	eventRepo := buildEventRepository(env, esClient)
	_ = eventRepo

	db, err := projection.NewProjectionDB(env.DBDSN)
	if err != nil {
		return nil, fmt.Errorf("open projection db: %w", err)
	}

	router := mux.NewRouter()
	server.RegisterRoutes(router)

	// esb:inject:app-init

	return &App{
		Env:     env,
		Handler: router,
		// esb:inject:app-return-fields
	}, nil
}

// buildEventRepository returns the HTTP adapter, the embedded
// SQLite adapter, or an error depending on EVENT_STORE_MODE. The
// esb-server branch requires a non-empty ESB_URL so we fail-fast
// at startup rather than letting handlers crash on a missing
// remote store.
func buildEventRepository(env *Env, client *eventstore.Client) domain.EventRepository {
	switch env.EventStoreMode {
	case "esb-server":
		if env.ESBURL == "" {
			panic("EVENT_STORE_MODE=esb-server requires ESB_URL to be set")
		}
		return repository.NewEventStoreAdapter(client)
	default:
		db, err := projection.NewProjectionDB(env.EventStoreDSN)
		if err != nil {
			panic(fmt.Sprintf("open embedded event store: %v", err))
		}
		if err := eventstore.MigrateLocalStore(db); err != nil {
			panic(fmt.Sprintf("migrate embedded event store: %v", err))
		}
		store := eventstore.NewLocalStore(db)
		return repository.NewLocalAdapter(store)
	}
}

// mustLoadKey reads private.pem or panics. Used at startup so a
// missing key is loud, not silent.
func mustLoadKey() *ecdsa.PrivateKey {
	key, err := loadPrivateKey("private.pem")
	if err != nil {
		panic(fmt.Sprintf("load private key: %v", err))
	}
	return key
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
