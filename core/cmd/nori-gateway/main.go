package main

import (
	"cmp"
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/oauth2/google"
	"nori/internal/gateway"
	"nori/internal/httpserver"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	config, bind, err := configuration(ctx)
	if err != nil {
		log.Fatal(err)
	}
	handler, err := gateway.New(config)
	if err != nil {
		log.Fatal(err)
	}
	log.Print("Nori client gateway ready")
	if err := httpserver.Listen(ctx, httpserver.New(ctx, bind, handler), 10*time.Second); err != nil {
		log.Fatal(err)
	}
}

func configuration(ctx context.Context) (gateway.Config, string, error) {
	var cfg gateway.Config
	upstream, err := url.Parse(os.Getenv("NORI_GATEWAY_UPSTREAM_URL"))
	if err != nil || upstream.Hostname() == "" || upstream.User != nil || upstream.Fragment != "" || upstream.RawQuery != "" {
		return cfg, "", fmt.Errorf("configure a valid NORI_GATEWAY_UPSTREAM_URL")
	}
	cfg.Upstream = upstream
	cfg.CallerKey = os.Getenv("NORI_GATEWAY_CALLER_KEY")
	cfg.PerMinute, err = strconv.Atoi(cmp.Or(os.Getenv("NORI_GATEWAY_REQUESTS_PER_MINUTE"), "120"))
	if err != nil || cfg.PerMinute < 1 {
		return cfg, "", fmt.Errorf("NORI_GATEWAY_REQUESTS_PER_MINUTE must be positive")
	}
	cfg.Burst, err = strconv.Atoi(cmp.Or(os.Getenv("NORI_GATEWAY_BURST"), "30"))
	if err != nil || cfg.Burst < 1 {
		return cfg, "", fmt.Errorf("NORI_GATEWAY_BURST must be positive")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.ResponseHeaderTimeout = 35 * time.Second
	cfg.Transport = transport
	bind := cmp.Or(os.Getenv("BIND_HOST"), "0.0.0.0")
	switch cmp.Or(os.Getenv("NORI_GATEWAY_TRANSPORT"), "google") {
	case "google":
		if upstream.Scheme != "https" {
			return cfg, "", fmt.Errorf("Google transport requires an HTTPS upstream")
		}
		cfg.ServiceToken, err = google.DefaultTokenSource(ctx, "https://www.googleapis.com/auth/cloud-platform")
		if err != nil {
			return cfg, "", fmt.Errorf("Google gateway identity is unavailable")
		}
	case "local":
		if upstream.Scheme != "http" || !net.ParseIP(upstream.Hostname()).IsLoopback() {
			return cfg, "", fmt.Errorf("local gateway transport requires a loopback HTTP upstream")
		}
		bind = "127.0.0.1"
	default:
		return cfg, "", fmt.Errorf("NORI_GATEWAY_TRANSPORT must be google or local")
	}
	return cfg, net.JoinHostPort(bind, cmp.Or(os.Getenv("PORT"), "8080")), nil
}
