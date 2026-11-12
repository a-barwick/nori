// Package settings loads every setting Nori's processes read, once, at startup. It is the only code that reads the
// process environment. Each process passes on only the values a package needs. .env.example at the repo root
// describes each setting.
package settings

import (
	"cmp"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/joho/godotenv"
	"nori/internal/serviceauth"
	"nori/internal/usage"
)

type Settings struct {
	Features    Features
	Cloud       string
	AWS         AWS
	ServiceAuth serviceauth.Config
	// BindHost and Port are where the API listens. Port is empty for a polling worker.
	BindHost, Port string
	Models         Models
	Metering       usage.Config
	Store          Store
	Computer       Computer
	Temporal       Temporal
	Wake           Wake
	Worker         Worker
	// SearchKey is the Brave Search key. Without it, web search reports itself unavailable.
	SearchKey string
	// TimeZone and Computer are filled from the user row, never the environment.
	TimeZone string
	// TaskBudgetUSD is recorded as the spending ceiling when a background task starts.
	TaskBudgetUSD float64
	Observability Observability
	Connections   Connections
}

// Connections configures connected accounts. Provider app credentials are project keys from the env file; account
// credentials go to the credential store, which exists only for local development until decision D1 is made.
type Connections struct {
	GoogleClientID, GoogleClientSecret string
	// MicrosoftClientID and MicrosoftClientSecret come from an Entra app registration (personal and work accounts).
	MicrosoftClientID, MicrosoftClientSecret string
	MicrosoftTenant                          string
	// RedirectURL is the OAuth callback through the web proxy, for example http://127.0.0.1:3211/api/v1/connections/oauth/callback.
	RedirectURL string
	// CredentialStore is "local" (file vault, development only, requires NORI_CLOUD=local), "database" (envelope-
	// encrypted rows, D1) or empty (unavailable). The database store wraps data keys with NORI_CREDENTIAL_KMS_KEY (a
	// Cloud KMS key) or, on the local stack only, NORI_CREDENTIAL_KEY_FILE.
	CredentialStore, CredentialDir      string
	CredentialKMSKey, CredentialKeyFile string
	// UnattendedWrites and UserSends are the interim D2 mechanisms; see internal/permissions.
	UnattendedWrites, UserSends string
	// MailRecipients limits who Nori may send email to: "self" (the default) sends only to the connected account's
	// own address, read from the account's profile at connect; "any" lifts the limit. Drafts are not limited.
	MailRecipients string
	// ProviderReplay names a directory of recorded provider responses (internal/connectors/replay). Local development
	// only: platform refuses it unless NORI_CLOUD=local, and no provider is contacted while it is set.
	ProviderReplay string
	// MCPCommands declares local stdio MCP servers by command_ref → argument vector (NORI_MCP_COMMANDS, a JSON
	// object). Honoured only with NORI_CLOUD=local; requests name a command_ref, never a command line.
	MCPCommands map[string][]string
	// Delivery transports for push and email destinations (A28). Unset leaves the channel without a transport.
	SMTPHost, SMTPPort, SMTPUsername, SMTPPassword, EmailFrom string
	VAPIDPublicKey, VAPIDPrivateKey, VAPIDSubject             string
	// DeliveryTestCAFile is a PEM root trusted, in addition to the system roots, by the push and email transports so
	// they can be verified against local sinks with self-signed certificates. Local development only.
	DeliveryTestCAFile string
}

// Features uses disabled flags so an existing caller with zero settings keeps Nori enabled.
type Features struct {
	DisableBackgroundTasks, DisableJev, DisableComputer bool
}

type AWS struct {
	Region, WakeQueueURL string
}

type Models struct {
	// OpenAIKey serves conversation replies and, with the direct Terra ID, tasks and summaries.
	OpenAIKey, JevKey, OpenRouterKey string
	// Outer is the outer conversation model and Inner the task worker model.
	Outer, Inner string
	// Provider selects the deployment-owned gateway when set to "gateway".
	Provider, GatewayURL, GatewayKey string
	// TestURL sends every model request to a loopback HTTP stub instead of the provider. Local development only
	// (NORI_MODEL_TEST_URL, refused unless NORI_CLOUD=local), for checking timeouts and prompts without provider keys.
	TestURL string
}

func (m Models) UsesGateway() bool { return m.Provider == "gateway" }

func (m Models) UsesOpenAIForTasks() bool { return !m.UsesGateway() && m.Inner == "gpt-6.1-terra" }

func (m Models) TaskCredential() (name, key string) {
	if m.UsesGateway() {
		return "NORI_MODEL_GATEWAY_API_KEY", m.GatewayKey
	}
	if m.UsesOpenAIForTasks() {
		return "OPENAI_API_KEY", m.OpenAIKey
	}
	return "OPENROUTER_API_KEY", m.OpenRouterKey
}

type Store struct {
	Backend, PostgresURL string
	// Project is the Google Cloud project and Database its Firestore database. Neither has a default.
	Project, Database string
	// ArtifactBucket is the Cloud Storage bucket for file bytes.
	ArtifactBucket string
	// StateEmulator points app data at a loopback Firestore emulator.
	// FirestoreEmulator points the whole process at one for local development.
	StateEmulator, FirestoreEmulator string
	// LocalArtifactDir keeps file bytes in a local directory, for development.
	LocalArtifactDir string
}

type Computer struct {
	// LocalURL selects a loopback development computer. Otherwise Zone and Instance name a Compute Engine instance
	// in the store's project.
	LocalURL, Zone, Instance string
}

type Temporal struct {
	Address, Namespace string
	// APIKey, or the Secret Manager version that APIKeySecret names, signs in to Temporal Cloud.
	APIKey, APIKeySecret string
	TaskQueue            string
	// DeploymentName is empty when unset: worker registration requires it, and other code uses nori-task.
	DeploymentName, BuildID string
	// Local is a development server with no TLS or API key. CloudRunID tags a Cloud Run worker's identity.
	Local, CloudRunID bool
}

func (s Temporal) EffectiveDeploymentName() string {
	return cmp.Or(s.DeploymentName, "nori-task")
}

// Validate checks connection settings without opening a connection or resolving a secret.
func (s Temporal) Validate() error {
	for _, setting := range [][2]string{{"TEMPORAL_ADDRESS", s.Address}, {"TEMPORAL_NAMESPACE", s.Namespace}, {"TEMPORAL_TASK_QUEUE", s.TaskQueue}} {
		if _, err := Required(setting[0], setting[1]); err != nil {
			return err
		}
	}
	if !s.Local && strings.TrimSpace(s.APIKey) == "" && strings.TrimSpace(s.APIKeySecret) == "" {
		return fmt.Errorf("TEMPORAL_API_KEY or TEMPORAL_API_KEY_SECRET_VERSION must be configured")
	}
	return nil
}

type Wake struct {
	// ConversationQueue and RuntimeURL are the hosted conversation wake. Caller is the service account of every
	// wake.
	ConversationQueue, RuntimeURL, Caller string
	WorkerQueue, Emulator                 string
	workerServices                        func() (map[string]string, error)
}

func (w Wake) WorkerServices() (map[string]string, error) {
	if w.workerServices == nil {
		return nil, nil
	}
	return w.workerServices()
}

func wakeFromEnvironment() Wake {
	w := Wake{
		ConversationQueue: os.Getenv("NORI_WAKE_QUEUE"), RuntimeURL: os.Getenv("NORI_RUNTIME_URL"), Caller: os.Getenv("NORI_WAKE_SERVICE_ACCOUNT"),
		WorkerQueue: os.Getenv("NORI_WORKER_WAKE_QUEUE"), Emulator: os.Getenv("CLOUD_TASKS_EMULATOR_HOST"),
	}
	if raw := os.Getenv("NORI_WORKER_SERVICES"); raw != "" {
		w.workerServices = sync.OnceValues(func() (map[string]string, error) {
			var routes map[string]string
			if err := json.Unmarshal([]byte(raw), &routes); err != nil || len(routes) == 0 {
				return nil, fmt.Errorf("NORI_WORKER_SERVICES must be a nonempty JSON map of worker builds to service URLs")
			}
			return routes, nil
		})
	}
	return w
}

type Worker struct {
	// PollWindow and IdleWindow bound a request worker's slice, as Go durations.
	PollWindow, IdleWindow string
}

type Observability struct {
	SentryDSN, SentryEnvironment, SentryRelease string
	// SampleRate and the hourly limits stay text: an invalid value falls back to its default.
	SampleRate, ErrorsPerHour, LogsPerHour, MetricsPerHour string
	// Revision and Service are set by Cloud Run.
	Revision, Service                                                                    string
	ArizeProject, ArizeProjectURL, ArizeSpace, ArizeKey, ArizeEndpoint, ArizeCertificate string
	// CaptureContent sends redacted prompt and reply text to Arize.
	CaptureContent bool
	// Credentials are values redacted from anything exported.
	Credentials []string
}

// Load reads NORI_ENV_FILE (default ../.env), then NORI_TASK_ENV_FILE when it names a separate task configuration,
// and returns the settings. Missing files are fine in deployed environments; malformed files are not. Already-set
// variables win. Management credentials are removed first and never loaded.
func Load() (Settings, error) {
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
	for _, name := range []string{"NORI_BACKGROUND_TASKS_ENABLED", "NORI_JEV_ENABLED", "NORI_COMPUTER_ENABLED"} {
		if v := os.Getenv(name); v != "" && v != "true" && v != "false" {
			return Settings{}, fmt.Errorf("%s must be true or false", name)
		}
	}
	s := FromEnvironment()
	switch s.Cloud {
	case "gcp", "aws", "local":
	default:
		return Settings{}, fmt.Errorf("NORI_CLOUD must be gcp, aws or local")
	}
	if math.IsNaN(s.TaskBudgetUSD) || math.IsInf(s.TaskBudgetUSD, 0) || s.TaskBudgetUSD <= 0 {
		return Settings{}, fmt.Errorf("NORI_TASK_BUDGET_USD must be a positive dollar amount")
	}
	switch s.Store.Backend {
	case "firestore":
	case "postgres":
		if _, err := Required("NORI_POSTGRES_URL", s.Store.PostgresURL); err != nil {
			return Settings{}, err
		}
	default:
		return Settings{}, fmt.Errorf("NORI_STORE_BACKEND must be firestore or postgres")
	}
	if err := s.Metering.Validate(); err != nil {
		return Settings{}, err
	}
	if err := s.Models.validateGateway(); err != nil {
		return Settings{}, err
	}
	if s.Models.TestURL != "" {
		u, err := url.Parse(s.Models.TestURL)
		if err != nil || s.Cloud != "local" || u.Scheme != "http" || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") {
			return Settings{}, fmt.Errorf("NORI_MODEL_TEST_URL is for local development only: an http loopback URL with NORI_CLOUD=local")
		}
	}
	return s, nil
}

// FromEnvironment supplies settings for the computer agent without loading a project env file.
func connectionsFromEnvironment(localArtifacts string) Connections {
	dir := os.Getenv("NORI_CREDENTIAL_DIR")
	if dir == "" && localArtifacts != "" {
		dir = filepath.Join(localArtifacts, "credentials")
	}
	return Connections{
		GoogleClientID: os.Getenv("GOOGLE_CALENDAR_CLIENT_ID"), GoogleClientSecret: os.Getenv("GOOGLE_CALENDAR_CLIENT_SECRET"),
		MicrosoftClientID: os.Getenv("MICROSOFT_CLIENT_ID"), MicrosoftClientSecret: os.Getenv("MICROSOFT_CLIENT_SECRET"),
		MicrosoftTenant: os.Getenv("MICROSOFT_TENANT"),
		RedirectURL:     os.Getenv("NORI_OAUTH_REDIRECT_URL"), CredentialStore: os.Getenv("NORI_CREDENTIAL_STORE"), CredentialDir: dir,
		CredentialKMSKey: os.Getenv("NORI_CREDENTIAL_KMS_KEY"), CredentialKeyFile: os.Getenv("NORI_CREDENTIAL_KEY_FILE"),
		UnattendedWrites: get("NORI_AUTOMATION_UNATTENDED", "confirm_writes"), UserSends: get("NORI_USER_SENDS", "allow"),
		MailRecipients: get("NORI_MAIL_RECIPIENTS", "self"),
		ProviderReplay: os.Getenv("NORI_PROVIDER_REPLAY"),
		MCPCommands:    mcpCommands(os.Getenv("NORI_MCP_COMMANDS")),
		SMTPHost:       os.Getenv("NORI_SMTP_HOST"), SMTPPort: os.Getenv("NORI_SMTP_PORT"), SMTPUsername: os.Getenv("NORI_SMTP_USERNAME"),
		SMTPPassword: os.Getenv("NORI_SMTP_PASSWORD"), EmailFrom: os.Getenv("NORI_EMAIL_FROM"),
		VAPIDPublicKey: os.Getenv("NORI_VAPID_PUBLIC_KEY"), VAPIDPrivateKey: os.Getenv("NORI_VAPID_PRIVATE_KEY"), VAPIDSubject: os.Getenv("NORI_VAPID_SUBJECT"),
		DeliveryTestCAFile: os.Getenv("NORI_DELIVERY_TEST_CA_FILE"),
	}
}

func FromEnvironment() Settings {
	taskBudget, err := strconv.ParseFloat(get("NORI_TASK_BUDGET_USD", "1"), 64)
	if err != nil {
		taskBudget = math.NaN()
	}
	cloud := get("NORI_CLOUD", "gcp")
	defaultStore := "firestore"
	if cloud == "aws" || cloud == "local" {
		defaultStore = "postgres"
	}
	store := Store{
		Backend: get("NORI_STORE_BACKEND", defaultStore), PostgresURL: os.Getenv("NORI_POSTGRES_URL"),
		Project: os.Getenv("NORI_PROJECT_ID"), Database: os.Getenv("FIRESTORE_DATABASE_ID"), ArtifactBucket: os.Getenv("ARTIFACT_BUCKET"),
		StateEmulator: os.Getenv("NORI_STATE_EMULATOR_HOST"), FirestoreEmulator: os.Getenv("FIRESTORE_EMULATOR_HOST"),
		LocalArtifactDir: os.Getenv("NORI_LOCAL_ARTIFACT_DIR"),
	}
	temporal := Temporal{
		Address: os.Getenv("TEMPORAL_ADDRESS"), Namespace: os.Getenv("TEMPORAL_NAMESPACE"),
		APIKey: os.Getenv("TEMPORAL_API_KEY"), APIKeySecret: os.Getenv("TEMPORAL_API_KEY_SECRET_VERSION"),
		TaskQueue: get("TEMPORAL_TASK_QUEUE", "nori-tasks"), DeploymentName: os.Getenv("TEMPORAL_DEPLOYMENT_NAME"),
		BuildID: os.Getenv("TEMPORAL_BUILD_ID"), Local: os.Getenv("TEMPORAL_LOCAL") == "true",
		CloudRunID: os.Getenv("TEMPORAL_CLOUD_RUN_ID") == "true",
	}
	var credentials []string
	for _, name := range []string{"NORI_MODEL_GATEWAY_API_KEY", "OPENAI_API_KEY", "OPENROUTER_API_KEY", "JEV_API_KEY", "TEMPORAL_API_KEY", "TEMPORAL_BILLING_API_KEY", "SENTRY_AUTH_TOKEN", "ARIZE_API_KEY", "NORI_USAGE_TOKEN", "NORI_POSTGRES_URL", "NORI_SERVICE_TOKEN_KEY", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "NORI_SMTP_PASSWORD", "NORI_VAPID_PRIVATE_KEY", "MICROSOFT_CLIENT_SECRET"} {
		if value := os.Getenv(name); value != "" {
			credentials = append(credentials, value)
		}
	}
	return Settings{
		Connections: connectionsFromEnvironment(store.LocalArtifactDir),
		Features:    Features{DisableBackgroundTasks: os.Getenv("NORI_BACKGROUND_TASKS_ENABLED") == "false", DisableJev: os.Getenv("NORI_JEV_ENABLED") == "false", DisableComputer: os.Getenv("NORI_COMPUTER_ENABLED") == "false"},
		Cloud:       cloud,
		AWS:         AWS{Region: get("AWS_REGION", os.Getenv("AWS_DEFAULT_REGION")), WakeQueueURL: os.Getenv("NORI_AWS_WAKE_QUEUE_URL")},
		ServiceAuth: serviceauth.Config{Key: os.Getenv("NORI_SERVICE_TOKEN_KEY"), Audience: get("NORI_SERVICE_AUDIENCE", "nori")},
		BindHost:    get("BIND_HOST", "127.0.0.1"), Port: os.Getenv("PORT"),
		Models:        modelsFromEnvironment(),
		Store:         store,
		Metering:      usage.Config{Mode: get("NORI_USAGE_METER", "store"), Endpoint: os.Getenv("NORI_USAGE_ENDPOINT"), Token: os.Getenv("NORI_USAGE_TOKEN")},
		Computer:      Computer{},
		Temporal:      temporal,
		Wake:          wakeFromEnvironment(),
		Worker:        Worker{PollWindow: get("NORI_WORKER_POLL_WINDOW", "240s"), IdleWindow: get("NORI_WORKER_IDLE_WINDOW", "15s")},
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

func managementCredential(name string) bool {
	switch name {
	case "OPENROUTER_MANAGEMENT_KEY", "TEMPORAL_BILLING_API_KEY", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "NORI_SPEND_SIGNING_KEY", "NORI_SPEND_FEATURE_ISSUER_TOKEN", "NORI_SPEND_CI_ISSUER_TOKEN", "SENTRY_AUTH_TOKEN":
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

func mcpCommands(raw string) map[string][]string {
	commands := map[string][]string{}
	if raw == "" {
		return commands
	}
	if err := json.Unmarshal([]byte(raw), &commands); err != nil {
		log.Printf("NORI_MCP_COMMANDS is not a JSON object of command_ref to argument arrays; no stdio servers are declared")
		return map[string][]string{}
	}
	for ref, argv := range commands {
		if len(argv) == 0 {
			delete(commands, ref)
		}
	}
	return commands
}
