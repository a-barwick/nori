// Package settings loads every setting Nori's processes read, once, at startup. It is the only code that reads the
// process environment. Each process passes on only the values a package needs. .env.example at the repo root
// describes each setting.
package settings

import (
	"fmt"
	"os"
	"strings"

	"github.com/joho/godotenv"
)

type Settings struct {
	// BindHost and Port are where the API listens. Port is empty for a polling worker.
	BindHost, Port string
	Models         Models
	Store          Store
	Computer       Computer
	Temporal       Temporal
	Wake           Wake
	Worker         Worker
	Calendar       Calendar
	// SearchKey is the Brave Search key and RoutesKey the Google Routes key. Without one, that tool reports itself
	// unavailable.
	SearchKey, RoutesKey string
	// TimeZone is the IANA zone for dates, schedules and calendar checks.
	TimeZone string
	// QuietHours hold back notification emails, as HH:MM-HH:MM.
	QuietHours string
	// AppURL is the web app that notification emails link to. NotifyEmail is their recipient: empty means the
	// connected account's own address, "off" sends none.
	AppURL, NotifyEmail string
	// EnvFile is the configuration file Load read.
	EnvFile       string
	Spend         Spend
	Observability Observability
	Test          Test
}

type Models struct {
	// OpenAIKey is the outer conversation's key, JevKey the intent classifier's, and OpenRouterKey the task worker's
	// and the conversation summary's.
	OpenAIKey, JevKey, OpenRouterKey string
	// Outer is the outer conversation model and Inner the task worker model.
	Outer, Inner string
}

type Store struct {
	// Project is the Google Cloud project and Database its Firestore database. Neither has a default.
	Project, Database string
	// ArtifactBucket is the Cloud Storage bucket for file bytes.
	ArtifactBucket string
	// StateEmulator points app data at a loopback Firestore emulator while spend uses the real database.
	// FirestoreEmulator points the whole process at one, for unpaid tests.
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

type Wake struct {
	// ConversationQueue and RuntimeURL are the hosted conversation wake. Caller is the service account of every
	// wake.
	ConversationQueue, RuntimeURL, Caller string
	// WorkerServices is a JSON map of worker build to route, and WorkerQueue their wake queue. Emulator is a
	// loopback Cloud Tasks emulator.
	WorkerServices, WorkerQueue, Emulator string
}

type Worker struct {
	// PollWindow and IdleWindow bound a request worker's slice, as Go durations.
	PollWindow, IdleWindow string
}

type Calendar struct {
	ClientID, ClientSecret, RefreshToken string
	// Account names the connected Google account. WriteID is the calendar writes go to.
	Account, WriteID string
}

type Spend struct {
	// Scope and Profile select a development spend allowance. Database holds the shared budget.
	Scope, Profile, Database string
	// Environment is "sandbox" for a sandbox deployment, which must run under an allowance.
	Environment string
	// Project and StoreDatabase are the store's, for the ledger and the sandbox check.
	Project, StoreDatabase string
}

type Observability struct {
	SentryDSN, SentryEnvironment, SentryRelease string
	// SampleRate and the hourly limits stay text: an invalid value falls back to its default.
	SampleRate, ErrorsPerHour, LogsPerHour, MetricsPerHour string
	// Revision and Service are set by Cloud Run. BuildID is the Temporal worker build.
	Revision, Service, BuildID                                                           string
	ArizeProject, ArizeProjectURL, ArizeSpace, ArizeKey, ArizeEndpoint, ArizeCertificate string
	// CaptureContent sends redacted prompt and reply text to Arize.
	CaptureContent bool
	// Credentials are values redacted from anything exported.
	Credentials []string
}

// Test settings point a local test stack at loopback fakes. Each requires Temporal.Local.
type Test struct {
	OpenAIURL, ModelURL, JevURL, GoogleURL, RoutesURL string
	// Now is an RFC 3339 time the clock starts from.
	Now string
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
	s := FromEnvironment()
	s.EnvFile = file
	return s, nil
}

// FromEnvironment reads the settings from the process environment alone, for a process that loads no file.
func FromEnvironment() Settings {
	store := Store{
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
	for _, name := range []string{"OPENAI_API_KEY", "OPENROUTER_API_KEY", "JEV_API_KEY", "TEMPORAL_API_KEY", "TEMPORAL_BILLING_API_KEY", "SENTRY_AUTH_TOKEN", "ARIZE_API_KEY"} {
		if value := os.Getenv(name); value != "" {
			credentials = append(credentials, value)
		}
	}
	return Settings{
		BindHost: get("BIND_HOST", "127.0.0.1"), Port: os.Getenv("PORT"),
		Models: Models{
			OpenAIKey: os.Getenv("OPENAI_API_KEY"), JevKey: os.Getenv("JEV_API_KEY"), OpenRouterKey: os.Getenv("OPENROUTER_API_KEY"),
			Outer: get("NORI_AGENT_MODEL", "gpt-6.1-sol"), Inner: get("MODEL_NAME", "openai/gpt-6.1-terra"),
		},
		Store:    store,
		Computer: Computer{LocalURL: os.Getenv("NORI_LOCAL_COMPUTER_URL"), Zone: os.Getenv("COMPUTER_ZONE"), Instance: os.Getenv("COMPUTER_INSTANCE")},
		Temporal: temporal,
		Wake: Wake{
			ConversationQueue: os.Getenv("NORI_WAKE_QUEUE"), RuntimeURL: os.Getenv("NORI_RUNTIME_URL"), Caller: os.Getenv("NORI_WAKE_SERVICE_ACCOUNT"),
			WorkerServices: os.Getenv("NORI_WORKER_SERVICES"), WorkerQueue: os.Getenv("NORI_WORKER_WAKE_QUEUE"), Emulator: os.Getenv("CLOUD_TASKS_EMULATOR_HOST"),
		},
		Worker: Worker{PollWindow: get("NORI_WORKER_POLL_WINDOW", "240s"), IdleWindow: get("NORI_WORKER_IDLE_WINDOW", "15s")},
		Calendar: Calendar{
			ClientID: os.Getenv("GOOGLE_CALENDAR_CLIENT_ID"), ClientSecret: os.Getenv("GOOGLE_CALENDAR_CLIENT_SECRET"),
			RefreshToken: os.Getenv("GOOGLE_CALENDAR_REFRESH_TOKEN"), Account: get("GOOGLE_CALENDAR_ACCOUNT", "the connected Google account"),
			WriteID: os.Getenv("GOOGLE_CALENDAR_WRITE_ID"),
		},
		SearchKey: os.Getenv("BRAVE_SEARCH_API_KEY"), RoutesKey: os.Getenv("GOOGLE_ROUTES_API_KEY"),
		TimeZone: get("NORI_TIMEZONE", "America/Los_Angeles"), QuietHours: get("NORI_QUIET_HOURS", "22:00-07:00"),
		AppURL: os.Getenv("NORI_APP_URL"), NotifyEmail: os.Getenv("NORI_NOTIFY_EMAIL"),
		Spend: Spend{
			Scope: os.Getenv("NORI_SPEND_SCOPE"), Profile: os.Getenv("NORI_SPEND_PROFILE"), Database: get("NORI_SPEND_DATABASE", "nori-sandbox"),
			Environment: os.Getenv("NORI_ENVIRONMENT"), Project: store.Project, StoreDatabase: store.Database,
		},
		Observability: Observability{
			SentryDSN: strings.TrimSpace(os.Getenv("SENTRY_DSN")), SentryEnvironment: os.Getenv("SENTRY_ENVIRONMENT"), SentryRelease: os.Getenv("SENTRY_RELEASE"),
			SampleRate: os.Getenv("SENTRY_TRACES_SAMPLE_RATE"), ErrorsPerHour: os.Getenv("NORI_SENTRY_ERRORS_PER_HOUR"),
			LogsPerHour: os.Getenv("NORI_SENTRY_LOGS_PER_HOUR"), MetricsPerHour: os.Getenv("NORI_SENTRY_METRICS_PER_HOUR"),
			Revision: os.Getenv("K_REVISION"), Service: os.Getenv("K_SERVICE"), BuildID: temporal.BuildID,
			ArizeProject: get("ARIZE_PROJECT_NAME", "nori"), ArizeProjectURL: strings.TrimSpace(os.Getenv("ARIZE_PROJECT_URL")),
			ArizeSpace: os.Getenv("ARIZE_SPACE_ID"), ArizeKey: os.Getenv("ARIZE_API_KEY"),
			ArizeEndpoint: get("ARIZE_OTLP_ENDPOINT", "https://otlp.arize.com/v1/traces"), ArizeCertificate: os.Getenv("ARIZE_OTLP_CERTIFICATE"),
			CaptureContent: os.Getenv("NORI_ARIZE_CAPTURE_CONTENT") == "true", Credentials: credentials,
		},
		Test: Test{
			OpenAIURL: os.Getenv("NORI_E2E_OPENAI_URL"), ModelURL: os.Getenv("NORI_E2E_MODEL_URL"), JevURL: os.Getenv("NORI_E2E_JEV_URL"),
			GoogleURL: os.Getenv("NORI_E2E_GOOGLE_URL"), RoutesURL: os.Getenv("NORI_E2E_ROUTES_URL"), Now: os.Getenv("NORI_E2E_NOW"),
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
