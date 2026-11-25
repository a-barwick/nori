// Package settings loads every setting Nori's processes read, once, at startup. It is the only code that reads the
// process environment. Each process passes on only the values a package needs. .env.example at the repo root
// describes each setting.
package settings

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"nori/internal/llmguard"
	"nori/internal/serviceauth"
	"nori/internal/usage"
)

type Settings struct {
	Features Features
	Cloud    string
	// EnvProfile selects key isolation profile: "prod", "dev", or "test".
	EnvProfile string
	// Role selects which runtime subsystems cmd/nori runs: "all" (default), "api", or "worker".
	Role        string
	AWS         AWS
	ServiceAuth serviceauth.Config
	// BindHost and Port are where the API listens.
	BindHost, Port string
	Models         Models
	Metering       usage.Config
	Store          Store
	// SearchKey is the Brave Search key. Without it, web search reports itself unavailable.
	SearchKey string
	// TimeZone is filled from the user row, never the environment.
	TimeZone string
	// TaskBudgetUSD is recorded as the spending ceiling when a background task starts.
	TaskBudgetUSD float64
	Observability Observability
	Connections   Connections
}

// Connections configures connected accounts and MCP servers.
type Connections struct {
	// RedirectURL is the OAuth callback through the web proxy, for example http://127.0.0.1:3211/api/v1/connections/oauth/callback.
	RedirectURL string
	// CredentialStore is "database" (envelope-encrypted rows, D1) or empty (unavailable). The database store wraps
	// data keys with NORI_CREDENTIAL_KEY / NORI_CREDENTIAL_KEY_SECRET (base64 or hex 32-byte key, optionally from
	// AWS Secrets Manager) or, on the local stack only, NORI_CREDENTIAL_KEY_FILE.
	CredentialStore                                       string
	CredentialKey, CredentialKeySecret, CredentialKeyFile string
	// UnattendedWrites and UserSends are the interim D2 mechanisms; see internal/permissions.
	UnattendedWrites, UserSends string
	// MCPCommands declares local stdio MCP servers by command_ref -> argument vector (NORI_MCP_COMMANDS, a JSON
	// object). Honoured only with NORI_CLOUD=local; requests name a command_ref, never a command line.
	MCPCommands map[string][]string
}

// Features uses disabled flags so an existing caller with zero settings keeps Nori enabled.
type Features struct {
	DisableBackgroundTasks, DisableJev bool
}

type AWS struct {
	Region string
}

type Models struct {
	// EnvProfile is the resolved key isolation profile ("prod", "dev", or "test").
	EnvProfile string
	// Provider keys resolved according to EnvProfile (never falling back across profiles in test mode).
	OpenAIKey, AnthropicKey, JevKey, OpenRouterKey string
	// Outer is the outer conversation model and Inner the task worker model.
	Outer, Inner string
	// OuterReasoning and InnerReasoning optionally override default reasoning effort ("low", "medium", "high").
	OuterReasoning, InnerReasoning string
	// Provider sets the default model provider ("openai", "anthropic", "openrouter", "gateway").
	// OuterProvider and InnerProvider optionally override the provider per tier.
	Provider, OuterProvider, InnerProvider string
	GatewayURL, GatewayKey                 string
	// MaxUSD optionally sets a process-wide hard estimated spend ceiling in llmguard (NORI_LLM_MAX_USD).
	MaxUSD float64
	// TestURL sends every model request to a loopback HTTP stub instead of the provider. Local development only
	// (NORI_MODEL_TEST_URL, refused unless NORI_CLOUD=local), for checking timeouts and prompts without provider keys.
	TestURL string
}

type Store struct {
	Backend, PostgresURL string
	// PostgresListenURL optionally overrides PostgresURL for dedicated LISTEN connections
	// (for deployments where PostgresURL routes through RDS Proxy or PgBouncer transaction pooling).
	PostgresListenURL string
	// Postgres pool and query tuning; zero values fall back to postgres backend defaults.
	PostgresMaxConns          int32
	PostgresMinConns          int32
	PostgresMaxConnLifetime   time.Duration
	PostgresMaxConnIdleTime   time.Duration
	PostgresHealthCheckPeriod time.Duration
	PostgresStatementTimeout  time.Duration
	// ArtifactBucket is the S3 bucket for file bytes.
	ArtifactBucket string
	// LocalArtifactDir keeps file bytes in a local directory, for development.
	LocalArtifactDir string
}

// ValidRole reports whether role is one of "all", "api", or "worker".
func ValidRole(role string) bool {
	switch role {
	case "all", "api", "worker":
		return true
	default:
		return false
	}
}

type Observability struct {
	SentryDSN, SentryEnvironment, SentryRelease string
	// SampleRate and the hourly limits stay text: an invalid value falls back to its default.
	SampleRate, ErrorsPerHour, LogsPerHour, MetricsPerHour string
	// Revision and Service optionally come from the hosting platform.
	Revision, Service                                                                    string
	ArizeProject, ArizeProjectURL, ArizeSpace, ArizeKey, ArizeEndpoint, ArizeCertificate string
	// CaptureContent sends redacted prompt and reply text to Arize.
	CaptureContent bool
	// Credentials are values redacted from anything exported.
	Credentials []string
}

// Load reads NORI_ENV_FILE (default ../.env), then NORI_TASK_ENV_FILE when it names a separate task configuration,
// and returns the settings with NORI_CLOUD defaulting to "aws". Missing files are fine in deployed environments;
// malformed files are not. Already-set variables win. Management credentials are removed first and never loaded.
func Load() (Settings, error) {
	return LoadDefaultCloud("aws")
}

// LoadDefaultCloud behaves like Load, using defaultCloud when NORI_CLOUD is unset.
func LoadDefaultCloud(defaultCloud string) (Settings, error) {
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if managementCredential(name) {
			_ = os.Unsetenv(name)
		}
	}
	file := get("NORI_ENV_FILE", "../.env")
	if err := loadFile(file); err != nil && !os.IsNotExist(err) {
		return Settings{}, fmt.Errorf("could not load %s; check its syntax and permissions", file)
	}
	if task := get("NORI_TASK_ENV_FILE", file); task != file {
		if err := loadFile(task); err != nil && !os.IsNotExist(err) {
			return Settings{}, fmt.Errorf("could not load task configuration %s", task)
		}
	}
	for _, name := range []string{"NORI_BACKGROUND_TASKS_ENABLED", "NORI_JEV_ENABLED"} {
		if v := os.Getenv(name); v != "" && v != "true" && v != "false" {
			return Settings{}, fmt.Errorf("%s must be true or false", name)
		}
	}
	s := fromEnvironment(defaultCloud)
	switch s.Cloud {
	case "aws", "local":
	default:
		return Settings{}, fmt.Errorf("NORI_CLOUD must be aws or local")
	}
	if !ValidEnvProfile(s.EnvProfile) {
		return Settings{}, fmt.Errorf("NORI_ENV_PROFILE must be prod, dev, or test")
	}
	if s.Cloud == "aws" && s.EnvProfile != "prod" {
		return Settings{}, fmt.Errorf("NORI_ENV_PROFILE must be prod when NORI_CLOUD=aws")
	}
	if s.Cloud == "local" && s.EnvProfile == "prod" {
		return Settings{}, fmt.Errorf("NORI_ENV_PROFILE=prod is not allowed when NORI_CLOUD=local")
	}
	if err := validateProfileKeyIsolation(s.EnvProfile); err != nil {
		return Settings{}, err
	}
	if !ValidRole(s.Role) {
		return Settings{}, fmt.Errorf("NORI_ROLE must be all, api, or worker")
	}
	if math.IsNaN(s.TaskBudgetUSD) || math.IsInf(s.TaskBudgetUSD, 0) || s.TaskBudgetUSD <= 0 {
		return Settings{}, fmt.Errorf("NORI_TASK_BUDGET_USD must be a positive dollar amount")
	}
	switch s.Store.Backend {
	case "postgres":
		if _, err := Required("NORI_POSTGRES_URL", s.Store.PostgresURL); err != nil {
			return Settings{}, err
		}
		if err := validatePostgresEnv(s.Store); err != nil {
			return Settings{}, err
		}
	default:
		return Settings{}, fmt.Errorf("NORI_STORE_BACKEND must be postgres")
	}
	if err := s.Metering.Validate(); err != nil {
		return Settings{}, err
	}
	if err := s.Models.validateModels(s.Cloud); err != nil {
		return Settings{}, err
	}
	if s.Models.TestURL != "" {
		u, err := url.Parse(s.Models.TestURL)
		if err != nil || s.Cloud != "local" || u.Scheme != "http" || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") {
			return Settings{}, fmt.Errorf("NORI_MODEL_TEST_URL is for local development only: an http loopback URL with NORI_CLOUD=local")
		}
	}
	if s.Models.MaxUSD > 0 {
		llmguard.Default().SetMaxEstimatedUSD(s.Models.MaxUSD)
	}
	return s, nil
}

// connectionsFromEnvironment loads connection, credential store, and MCP settings.
func connectionsFromEnvironment() Connections {
	return Connections{
		RedirectURL:         os.Getenv("NORI_OAUTH_REDIRECT_URL"),
		CredentialStore:     os.Getenv("NORI_CREDENTIAL_STORE"),
		CredentialKey:       os.Getenv("NORI_CREDENTIAL_KEY"),
		CredentialKeySecret: os.Getenv("NORI_CREDENTIAL_KEY_SECRET"),
		CredentialKeyFile:   os.Getenv("NORI_CREDENTIAL_KEY_FILE"),
		UnattendedWrites:    get("NORI_AUTOMATION_UNATTENDED", "confirm_writes"),
		UserSends:           get("NORI_USER_SENDS", "ask"),
		MCPCommands:         mcpCommands(os.Getenv("NORI_MCP_COMMANDS")),
	}
}

func FromEnvironment() Settings {
	return fromEnvironment("aws")
}

func fromEnvironment(defaultCloud string) Settings {
	taskBudget, err := strconv.ParseFloat(get("NORI_TASK_BUDGET_USD", "1"), 64)
	if err != nil {
		taskBudget = math.NaN()
	}
	cloud := get("NORI_CLOUD", defaultCloud)
	envProfile := resolveEnvProfile(cloud)
	role := get("NORI_ROLE", "all")
	store := Store{
		Backend:                   get("NORI_STORE_BACKEND", "postgres"),
		PostgresURL:               os.Getenv("NORI_POSTGRES_URL"),
		PostgresListenURL:         os.Getenv("NORI_POSTGRES_LISTEN_URL"),
		PostgresMaxConns:          parseEnvInt32("NORI_POSTGRES_MAX_CONNS"),
		PostgresMinConns:          parseEnvInt32("NORI_POSTGRES_MIN_CONNS"),
		PostgresMaxConnLifetime:   parseEnvDuration("NORI_POSTGRES_MAX_CONN_LIFETIME"),
		PostgresMaxConnIdleTime:   parseEnvDuration("NORI_POSTGRES_MAX_CONN_IDLE_TIME"),
		PostgresHealthCheckPeriod: parseEnvDuration("NORI_POSTGRES_HEALTH_CHECK_PERIOD"),
		PostgresStatementTimeout:  parseEnvDuration("NORI_POSTGRES_STATEMENT_TIMEOUT"),
		ArtifactBucket:            os.Getenv("ARTIFACT_BUCKET"),
		LocalArtifactDir:          os.Getenv("NORI_LOCAL_ARTIFACT_DIR"),
	}
	var credentials []string
	for _, name := range []string{
		"NORI_MODEL_GATEWAY_API_KEY",
		"NORI_GATEWAY_TOKEN",
		"OPENAI_API_KEY",
		"OPENAI_PROD_API_KEY",
		"OPENAI_DEV_API_KEY",
		"OPENAI_TEST_API_KEY",
		"ANTHROPIC_API_KEY",
		"ANTHROPIC_PROD_API_KEY",
		"ANTHROPIC_DEV_API_KEY",
		"ANTHROPIC_TEST_API_KEY",
		"OPENROUTER_API_KEY",
		"OPENROUTER_PROD_API_KEY",
		"OPENROUTER_DEV_API_KEY",
		"OPENROUTER_TEST_API_KEY",
		"JEV_API_KEY",
		"BRAVE_SEARCH_API_KEY",
		"SENTRY_AUTH_TOKEN",
		"ARIZE_API_KEY",
		"NORI_USAGE_TOKEN",
		"NORI_POSTGRES_URL",
		"NORI_POSTGRES_LISTEN_URL",
		"NORI_SERVICE_TOKEN_KEY",
		"NORI_CREDENTIAL_KEY",
		"AWS_SECRET_ACCESS_KEY",
		"AWS_SESSION_TOKEN",
	} {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			credentials = append(credentials, value)
		}
	}
	return Settings{
		Connections: connectionsFromEnvironment(),
		Features:    Features{DisableBackgroundTasks: os.Getenv("NORI_BACKGROUND_TASKS_ENABLED") == "false", DisableJev: os.Getenv("NORI_JEV_ENABLED") != "true"},
		Cloud:       cloud,
		EnvProfile:  envProfile,
		Role:        role,
		AWS:         AWS{Region: get("AWS_REGION", os.Getenv("AWS_DEFAULT_REGION"))},
		ServiceAuth: serviceauth.Config{
			Key:             os.Getenv("NORI_SERVICE_TOKEN_KEY"),
			Audience:        get("NORI_SERVICE_AUDIENCE", "nori"),
			GoogleClientIDs: splitCSV(os.Getenv("NORI_GOOGLE_OAUTH_CLIENT_IDS"), false),
			AllowedEmails:   splitCSV(os.Getenv("NORI_ALLOWED_EMAILS"), true),
		},
		BindHost:      get("BIND_HOST", "127.0.0.1"),
		Port:          os.Getenv("PORT"),
		Models:        modelsFromEnvironment(envProfile),
		Store:         store,
		Metering:      usage.Config{Mode: get("NORI_USAGE_METER", "store"), Endpoint: os.Getenv("NORI_USAGE_ENDPOINT"), Token: os.Getenv("NORI_USAGE_TOKEN"), MaxCallsPerHour: int(parseEnvInt32Default("NORI_USER_MAX_CALLS_PER_HOUR", 300)), MaxUSDPerDay: parseOptionalNonNegativeFloatDefault("NORI_USER_MAX_USD_PER_DAY", 15.0)},
		SearchKey:     os.Getenv("BRAVE_SEARCH_API_KEY"),
		TaskBudgetUSD: taskBudget,
		Observability: Observability{
			SentryDSN: strings.TrimSpace(os.Getenv("SENTRY_DSN")), SentryEnvironment: os.Getenv("SENTRY_ENVIRONMENT"), SentryRelease: os.Getenv("SENTRY_RELEASE"),
			SampleRate: os.Getenv("SENTRY_TRACES_SAMPLE_RATE"), ErrorsPerHour: os.Getenv("NORI_SENTRY_ERRORS_PER_HOUR"),
			LogsPerHour: os.Getenv("NORI_SENTRY_LOGS_PER_HOUR"), MetricsPerHour: os.Getenv("NORI_SENTRY_METRICS_PER_HOUR"),
			Revision: os.Getenv("K_REVISION"), Service: os.Getenv("K_SERVICE"),
			ArizeProject: get("ARIZE_PROJECT_NAME", "nori"), ArizeProjectURL: strings.TrimSpace(os.Getenv("ARIZE_PROJECT_URL")),
			ArizeSpace: os.Getenv("ARIZE_SPACE_ID"), ArizeKey: os.Getenv("ARIZE_API_KEY"),
			ArizeEndpoint: get("ARIZE_OTLP_ENDPOINT", "https://otlp.arize.com/v1/traces"), ArizeCertificate: os.Getenv("ARIZE_OTLP_CERTIFICATE"),
			CaptureContent: os.Getenv("NORI_ARIZE_CAPTURE_CONTENT") == "true", Credentials: credentials,
		},
	}
}

// MissingError names a required setting that is empty. It carries no value, so a process may print it.
type MissingError struct{ Name string }

func (e *MissingError) Error() string { return e.Name + " must be configured" }

// Required returns a setting's value, or a MissingError naming its variable when it is empty.
func Required(name, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", &MissingError{Name: name}
	}
	return value, nil
}

func get(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func parseEnvInt32(name string) int32 {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return 0
	}
	v, err := strconv.ParseInt(raw, 10, 32)
	if err != nil {
		return -1
	}
	return int32(v)
}

func parseEnvInt32Default(name string, fallback int32) int32 {
	if strings.TrimSpace(os.Getenv(name)) == "" {
		return fallback
	}
	return parseEnvInt32(name)
}

func parseEnvDuration(name string) time.Duration {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return 0
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return -1
	}
	return d
}

func validatePostgresEnv(s Store) error {
	for _, item := range []struct {
		name string
		val  int32
	}{
		{"NORI_POSTGRES_MAX_CONNS", s.PostgresMaxConns},
		{"NORI_POSTGRES_MIN_CONNS", s.PostgresMinConns},
	} {
		raw := strings.TrimSpace(os.Getenv(item.name))
		if raw != "" {
			v, err := strconv.ParseInt(raw, 10, 32)
			if err != nil || v < 0 {
				return fmt.Errorf("%s must be a non-negative integer", item.name)
			}
		}
		if item.val < 0 {
			return fmt.Errorf("%s must be a non-negative integer", item.name)
		}
	}
	if s.PostgresMaxConns > 0 && s.PostgresMinConns > s.PostgresMaxConns {
		return fmt.Errorf("NORI_POSTGRES_MIN_CONNS must not exceed NORI_POSTGRES_MAX_CONNS")
	}
	for _, item := range []struct {
		name string
		val  time.Duration
	}{
		{"NORI_POSTGRES_MAX_CONN_LIFETIME", s.PostgresMaxConnLifetime},
		{"NORI_POSTGRES_MAX_CONN_IDLE_TIME", s.PostgresMaxConnIdleTime},
		{"NORI_POSTGRES_HEALTH_CHECK_PERIOD", s.PostgresHealthCheckPeriod},
		{"NORI_POSTGRES_STATEMENT_TIMEOUT", s.PostgresStatementTimeout},
	} {
		raw := strings.TrimSpace(os.Getenv(item.name))
		if raw != "" {
			d, err := time.ParseDuration(raw)
			if err != nil || d < 0 {
				return fmt.Errorf("%s must be a non-negative Go duration", item.name)
			}
		}
		if item.val < 0 {
			return fmt.Errorf("%s must be a non-negative Go duration", item.name)
		}
	}
	return nil
}

func managementCredential(name string) bool {
	switch name {
	case "OPENROUTER_MANAGEMENT_KEY", "TEMPORAL_BILLING_API_KEY", "ANTHROPIC_AUTH_TOKEN", "NORI_SPEND_SIGNING_KEY", "NORI_SPEND_FEATURE_ISSUER_TOKEN", "NORI_SPEND_CI_ISSUER_TOKEN", "SENTRY_AUTH_TOKEN":
		return true
	}
	return strings.HasPrefix(name, "NORI_SPEND_LEASE_")
}

func loadFile(path string) error {
	values, err := godotenv.Read(path)
	if err != nil {
		return err
	}
	for name, value := range values {
		if _, exists := os.LookupEnv(name); !exists && !managementCredential(name) {
			if err := os.Setenv(name, value); err != nil {
				return err
			}
		}
	}
	return nil
}

func splitCSV(raw string, lower bool) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		v := strings.TrimSpace(part)
		if lower {
			v = strings.ToLower(v)
		}
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

func mcpCommands(raw string) map[string][]string {
	commands := map[string][]string{}
	if raw == "" {
		return commands
	}
	if err := json.Unmarshal([]byte(raw), &commands); err != nil {
		slog.Warn("NORI_MCP_COMMANDS is not a JSON object of command_ref to argument arrays; no stdio servers declared")
		return map[string][]string{}
	}
	for ref, argv := range commands {
		if len(argv) == 0 {
			delete(commands, ref)
		}
	}
	return commands
}
