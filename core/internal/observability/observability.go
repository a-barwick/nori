// Package observability configures a single OpenTelemetry provider and routes
// safe operational telemetry to Sentry and AI spans to Arize.
package observability

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/getsentry/sentry-go"
	sentryattr "github.com/getsentry/sentry-go/attribute"
	sentryotel "github.com/getsentry/sentry-go/otel"
	sentryotlp "github.com/getsentry/sentry-go/otel/otlp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/time/rate"
	"nori/internal/settings"
)

var tracerProvider *sdktrace.TracerProvider
var sentryEnabled atomic.Bool
var tracingEnabled atomic.Bool
var lastExportWarning atomic.Int64

// Init is called once per process, after loading its settings. Export
// failures never prevent Nori from starting or processing work.
func Init(ctx context.Context, service string, cfg settings.Observability) func(context.Context) error {
	sessionEnvironment = firstNonempty(cfg.SentryEnvironment, "development")
	captureContent = cfg.CaptureContent
	credentials = cfg.Credentials
	slog.SetDefault(slog.New(traceLogHandler{Handler: slog.NewJSONHandler(os.Stderr, nil)}))
	otel.SetTextMapPropagator(sentryTraceContext{})
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(error) {
		now := time.Now().Unix()
		last := lastExportWarning.Load()
		if now-last >= 60 && lastExportWarning.CompareAndSwap(last, now) {
			// Do not use slog or Sentry here: exporter failures must not recurse.
			fmt.Fprintln(os.Stderr, "observability: telemetry export failed")
		}
	}))

	release := firstNonempty(cfg.SentryRelease, cfg.Revision, cfg.BuildID, "development")
	environment := cfg.SentryEnvironment
	if environment == "" {
		environment = "development"
		if cfg.Service != "" {
			environment = "production"
		}
	}
	project := cfg.ArizeProject
	res := resource.NewWithAttributes("", attribute.String("service.name", service), attribute.String("service.version", release), attribute.String("deployment.environment.name", environment), attribute.String("openinference.project.name", project))
	arizeURL := arizeProjectURL(cfg.ArizeProjectURL)
	if cfg.ArizeProjectURL != "" && arizeURL == nil {
		fmt.Fprintln(os.Stderr, "observability: invalid Arize project URL; model span links disabled")
	}
	rateValue := sampleRate(cfg.SampleRate, 0.05)
	options := []sdktrace.TracerProviderOption{sdktrace.WithResource(res)}
	exporters := 0

	if dsn := cfg.SentryDSN; dsn != "" {
		errorLimit := hourlyLimiter(cfg.ErrorsPerHour, 50)
		logLimit := hourlyLimiter(cfg.LogsPerHour, 100)
		metricLimit := hourlyLimiter(cfg.MetricsPerHour, 200)
		err := sentry.Init(sentry.ClientOptions{
			Dsn: dsn, Release: release, Environment: environment,
			EnableTracing: true, TracesSampleRate: rateValue,
			HTTPClient: &http.Client{Timeout: 2 * time.Second}, MaxBreadcrumbs: -1,
			Integrations: func(integrations []sentry.Integration) []sentry.Integration {
				return append(integrations, sentryotel.NewOtelIntegration())
			},
			BeforeSend: func(event *sentry.Event, _ *sentry.EventHint) *sentry.Event {
				if !errorLimit.Allow() {
					diagnostic("sentry.errors", "rate_limited", 1)
					return nil
				}
				return scrubEvent(event)
			},
			// All traces use the filtered and rate-limited OpenTelemetry exporter.
			BeforeSendTransaction: func(*sentry.Event, *sentry.EventHint) *sentry.Event { return nil },
			BeforeBreadcrumb:      func(*sentry.Breadcrumb, *sentry.BreadcrumbHint) *sentry.Breadcrumb { return nil },
			BeforeSendLog: func(event *sentry.Log) *sentry.Log {
				if !eventNames[event.Body] {
					return nil
				}
				if !logLimit.Allow() {
					diagnostic("sentry.logs", "rate_limited", 1)
					return nil
				}
				for key, value := range event.Attributes {
					if !operationalKeys[key] && key != "sentry.release" && key != "sentry.environment" && key != "sentry.sdk.name" && key != "sentry.sdk.version" {
						delete(event.Attributes, key)
					} else if key == "nori.error.category" && !errorCategories[value.AsString()] {
						delete(event.Attributes, key)
					} else if value.Type() == sentryattr.STRING {
						event.Attributes[key] = sentryattr.StringValue(redactText(value.AsString(), 256))
					} else if value.Type() != sentryattr.BOOL && value.Type() != sentryattr.INT64 && value.Type() != sentryattr.FLOAT64 {
						delete(event.Attributes, key)
					}
				}
				return event
			},
			BeforeSendMetric: func(metric *sentry.Metric) *sentry.Metric {
				if !metricNames[metric.Name] {
					return nil
				}
				if !metricLimit.Allow() {
					diagnostic("sentry.metrics", "rate_limited", 1)
					return nil
				}
				for key := range metric.Attributes {
					if !metricDimensions[key] && key != "sentry.release" && key != "sentry.environment" && key != "sentry.sdk.name" && key != "sentry.sdk.version" {
						delete(metric.Attributes, key)
					}
				}
				return metric
			},
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, "observability: Sentry initialization failed; continuing without Sentry")
		} else {
			sentryEnabled.Store(true)
			exporter, err := sentryotlp.NewTraceExporter(ctx, dsn, sentryotlp.WithTimeout(2*time.Second), sentryotlp.WithRetry(otlptracehttp.RetryConfig{Enabled: false}))
			if err != nil {
				fmt.Fprintln(os.Stderr, "observability: Sentry trace exporter unavailable")
			} else {
				options = append(options, batch(&filteredExporter{inner: exporter, arizeProjectURL: arizeURL}))
				exporters++
			}
		}
	}

	space, key := cfg.ArizeSpace, cfg.ArizeKey
	if space != "" && key != "" {
		endpoint := cfg.ArizeEndpoint
		// Credentials must never be sent to an unencrypted collector.
		if !strings.HasPrefix(endpoint, "https://") {
			fmt.Fprintln(os.Stderr, "observability: Arize endpoint must use HTTPS")
		} else {
			arizeOptions := []otlptracehttp.Option{otlptracehttp.WithEndpointURL(endpoint), otlptracehttp.WithHeaders(map[string]string{"arize-space-id": space, "arize-api-key": key}), otlptracehttp.WithTimeout(2 * time.Second), otlptracehttp.WithRetry(otlptracehttp.RetryConfig{Enabled: false})}
			certificateValid := true
			// Trust an explicitly configured collector CA for Arize only. Global
			// OTLP certificate options also change Sentry's independent exporter.
			if path := cfg.ArizeCertificate; path != "" {
				pool, poolErr := x509.SystemCertPool()
				certificate, readErr := os.ReadFile(path)
				if poolErr != nil || readErr != nil || len(certificate) > 1<<20 || pool == nil || !pool.AppendCertsFromPEM(certificate) {
					certificateValid = false
					fmt.Fprintln(os.Stderr, "observability: invalid Arize collector certificate; Arize export disabled")
				} else {
					arizeOptions = append(arizeOptions, otlptracehttp.WithTLSClientConfig(&tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}))
				}
			}
			if certificateValid {
				exporter, err := otlptracehttp.New(ctx, arizeOptions...)
				if err != nil {
					fmt.Fprintln(os.Stderr, "observability: Arize exporter unavailable")
				} else {
					options = append(options, batch(&filteredExporter{inner: exporter, arize: true}))
					exporters++
				}
			}
		}
	} else if space != "" || key != "" {
		fmt.Fprintln(os.Stderr, "observability: both Arize credentials are required; Arize export disabled")
	}

	if exporters == 0 {
		options = append(options, sdktrace.WithSampler(sdktrace.NeverSample()))
	} else {
		options = append(options, sdktrace.WithSampler(excludedRequestSampler{sdktrace.ParentBased(sdktrace.TraceIDRatioBased(rateValue))}))
	}
	tracingEnabled.Store(exporters > 0)
	tracerProvider = sdktrace.NewTracerProvider(options...)
	otel.SetTracerProvider(tracerProvider)
	return func(ctx context.Context) error {
		err := tracerProvider.Shutdown(ctx)
		if sentryEnabled.Load() && !sentry.FlushWithContext(ctx) && err == nil {
			err = ctx.Err()
		}
		return err
	}
}

// TracingEnabled allows optional work solely for trace enrichment to be skipped.
func TracingEnabled() bool { return tracingEnabled.Load() }

type excludedRequestKey struct{}

// Suppress Firestore's automatic spans for polling and other excluded routes.
// Nori's own spans may still start when a session request repairs a wake.
type excludedRequestSampler struct{ sdktrace.Sampler }

func (s excludedRequestSampler) ShouldSample(p sdktrace.SamplingParameters) sdktrace.SamplingResult {
	if p.ParentContext.Value(excludedRequestKey{}) != nil &&
		(strings.HasPrefix(p.Name, "cloud.google.com/go/firestore.") || strings.HasPrefix(p.Name, "google.firestore.v1.Firestore/")) {
		return sdktrace.SamplingResult{Decision: sdktrace.Drop}
	}
	return s.Sampler.ShouldSample(p)
}

func (s excludedRequestSampler) Description() string {
	return "excluded-request(" + s.Sampler.Description() + ")"
}

func batch(exporter sdktrace.SpanExporter) sdktrace.TracerProviderOption {
	return sdktrace.WithBatcher(exporter, sdktrace.WithMaxQueueSize(256), sdktrace.WithMaxExportBatchSize(32), sdktrace.WithBatchTimeout(2*time.Second), sdktrace.WithExportTimeout(2*time.Second))
}

func hourlyLimiter(raw string, fallback int) *rate.Limiter {
	n := fallback
	if raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed >= 0 && parsed <= 10000 {
			n = parsed
		}
	}
	return rate.NewLimiter(rate.Limit(float64(n)/3600), n)
}

func sampleRate(raw string, fallback float64) float64 {
	if v, err := strconv.ParseFloat(raw, 64); err == nil && !math.IsNaN(v) && v >= 0 && v <= 1 {
		return v
	}
	return fallback
}

func firstNonempty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// Flush makes a bounded best effort before a request-bound instance goes idle.
func Flush(ctx context.Context) {
	// Independent exports share wall time, never consume each other's deadline.
	var work sync.WaitGroup
	if tracerProvider != nil {
		work.Add(1)
		go func() {
			defer work.Done()
			if tracerProvider.ForceFlush(ctx) != nil {
				diagnostic("otel", "flush_failed", 1)
			}
		}()
	}
	if sentryEnabled.Load() {
		work.Add(1)
		go func() {
			defer work.Done()
			if !sentry.FlushWithContext(ctx) {
				diagnostic("sentry", "flush_failed", 1)
			}
		}()
	}
	work.Wait()
}

// Shutdown gives process termination a short bounded opportunity to export.
func Shutdown(stop func(context.Context) error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = stop(ctx)
}

type suppressErrorCaptureKey struct{}

// SuppressErrorCapture leaves span status and logs intact when the caller
// records the same failure as an exception at its own boundary.
func SuppressErrorCapture(ctx context.Context) context.Context {
	return context.WithValue(ctx, suppressErrorCaptureKey{}, true)
}

// RecordErrorCategory captures a caller stack and safe grouping category,
// while never sending the original error text or request data.
func RecordErrorCategory(ctx context.Context, category string, err error, attrs ...attribute.KeyValue) {
	if err == nil {
		return
	}
	if !errorCategories[category] {
		category = "operation"
	}
	typeName := redactText(fmt.Sprintf("%T", err), 120)
	reason := errorReason(err)
	attrs = append(attrs, attribute.String("nori.error.reason", reason))
	span := trace.SpanFromContext(ctx)
	span.SetStatus(codes.Error, "operation failed")
	span.SetAttributes(attribute.String("error.type", typeName), attribute.String("nori.error.category", category))
	for _, attr := range attrs {
		if safe, ok := allowedAttribute(attr, false); ok {
			span.SetAttributes(safe)
		}
	}
	Event(ctx, "operation.failed", attribute.String("nori.error.category", category))
	if !sentryEnabled.Load() || ctx.Value(suppressErrorCaptureKey{}) != nil {
		return
	}
	event := sentry.NewEvent()
	event.Level = sentry.LevelError
	event.Exception = []sentry.Exception{{Type: "Nori/" + category, Value: category + " failed", Stacktrace: sentry.NewStacktrace()}}
	event.Fingerprint = []string{"{{ default }}", category, reason}
	event.Tags = map[string]string{"nori.error.category": category, "error.type": typeName}
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		event.Contexts["trace"] = sentry.Context{"trace_id": sc.TraceID().String(), "span_id": sc.SpanID().String()}
	}
	for _, attr := range attrs {
		if safe, ok := allowedAttribute(attr, false); ok {
			if safe.Key == "nori.error.category" || safe.Key == "error.type" {
				continue
			}
			event.Tags[string(safe.Key)] = safe.Value.Emit()
		}
	}
	if client := sentry.CurrentHub().Client(); client != nil {
		client.CaptureEvent(event, &sentry.EventHint{Context: ctx}, sentry.NewScope())
	}
}

func scrubEvent(event *sentry.Event) *sentry.Event {
	event.Request = nil
	event.User = sentry.User{}
	event.ServerName = ""
	event.Breadcrumbs = nil
	event.Attachments = nil
	event.Modules = nil
	event.Threads = nil
	event.DebugMeta = nil
	event.CheckIn = nil
	event.MonitorConfig = nil
	event.Logger = ""
	event.Transaction = ""
	event.Message = ""
	category := event.Tags["nori.error.category"]
	if !errorCategories[category] {
		category = "operation"
	}
	if event.Tags == nil {
		event.Tags = map[string]string{}
	}
	event.Tags["nori.error.category"] = category
	event.Fingerprint = []string{"{{ default }}", category, event.Tags["nori.error.reason"]}
	for i := range event.Exception {
		event.Exception[i].Type = "Nori/" + category
		event.Exception[i].Value = category + " failed"
		event.Exception[i].Module = ""
		event.Exception[i].Mechanism = nil
		if stack := event.Exception[i].Stacktrace; stack != nil {
			for j := range stack.Frames {
				stack.Frames[j].Vars = nil
				if stack.Frames[j].Filename == "" && stack.Frames[j].AbsPath != "" {
					stack.Frames[j].Filename = filepath.Base(stack.Frames[j].AbsPath)
				}
				stack.Frames[j].AbsPath = ""
				stack.Frames[j].ContextLine = ""
				stack.Frames[j].PreContext = nil
				stack.Frames[j].PostContext = nil
			}
		}
	}
	originalTrace := event.Contexts["trace"]
	traceContext := sentry.Context{}
	for _, key := range []string{"trace_id", "span_id"} {
		if value, ok := originalTrace[key]; ok {
			traceContext[key] = value
		}
	}
	event.Contexts = map[string]sentry.Context{}
	if len(traceContext) > 0 {
		event.Contexts["trace"] = traceContext
	}
	for key, value := range event.Tags {
		if !operationalKeys[key] {
			delete(event.Tags, key)
		} else {
			event.Tags[key] = redactText(value, 256)
		}
	}
	return event
}

// Fatal captures startup failures before exiting; log.Fatal skips deferred flushes.
func Fatal(err error) {
	RecordErrorCategory(context.Background(), "startup", err)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	Flush(ctx)
	fmt.Fprintln(os.Stderr, "nori: startup or process failure (details withheld)")
	os.Exit(1)
}

func errorReason(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "deadline"
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	var status interface{ HTTPStatusCode() int }
	if errors.As(err, &status) && status.HTTPStatusCode() >= 400 && status.HTTPStatusCode() <= 599 {
		return fmt.Sprintf("http_%d", status.HTTPStatusCode())
	}
	var network net.Error
	if errors.As(err, &network) {
		if network.Timeout() {
			return "timeout"
		}
		return "network"
	}
	return "other"
}

var diagnosticMu sync.Mutex
var diagnosticCounts = map[string]int{}
var diagnosticLast = map[string]time.Time{}

// Local diagnostics cannot recurse into either telemetry exporter. Names are
// static at call sites and counters include suppressed repetitions.
func diagnostic(destination, reason string, count int) {
	diagnosticMu.Lock()
	defer diagnosticMu.Unlock()
	key := destination + "/" + reason
	diagnosticCounts[key] += count
	if time.Since(diagnosticLast[key]) < time.Minute {
		return
	}
	fmt.Fprintf(os.Stderr, "observability: destination=%s reason=%s count=%d\n", destination, reason, diagnosticCounts[key])
	diagnosticLast[key] = time.Now()
}
