package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"nori/internal/computerd"
	"nori/internal/observability"
	"nori/internal/settings"
)

func main() {
	listen := flag.String("listen", "172.30.0.2:8090", "HTTP bind address")
	proxy := flag.Bool("proxy", false, "forward RPC to the guest instead of running tools")
	guest := flag.String("guest-url", "http://172.30.0.2:8090", "guest RPC address for host proxy")
	idle := flag.Duration("idle-timeout", 15*time.Minute, "host proxy idle stop delay; health checks do not extend it")
	tasks := flag.String("tasks", "/workspace/tasks", "task working directory")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	service := "nori-computer-guest"
	if *proxy {
		service = "nori-computer-host"
	}
	stopTelemetry := observability.Init(ctx, service, settings.FromEnvironment())
	defer observability.Shutdown(stopTelemetry)
	idleStop := make(chan struct{})
	var handler http.Handler
	if *proxy {
		if *idle <= 0 {
			observability.Fatal(errors.New("idle-timeout must be positive"))
		}
		target, err := url.Parse(*guest)
		if err != nil || target.Scheme != "http" || target.Host == "" || target.User != nil || target.RawQuery != "" || target.Fragment != "" || (target.Path != "" && target.Path != "/") {
			observability.Fatal(errors.New("guest-url must be an HTTP origin"))
		}
		target.Path = ""
		handler = computerd.Proxy(ctx, target, *idle, func() { close(idleStop); stop() })
	} else {
		var err error
		handler, err = computerd.New(*tasks)
		if err != nil {
			observability.Fatal(err)
		}
	}
	server := &http.Server{Addr: *listen, Handler: observability.HTTPHandler(handler), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 125 * time.Second, MaxHeaderBytes: 8192}
	go func() {
		<-ctx.Done()
		deadline, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(deadline)
	}()
	log.Printf("computer RPC listening on %s", *listen)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		observability.RecordErrorCategory(ctx, "http.listen", err)
		observability.Shutdown(stopTelemetry)
		observability.Fatal(err)
	}
	select {
	case <-idleStop:
		log.Print("computer idle; requesting graceful host stop")
		observability.Shutdown(stopTelemetry)
		os.Exit(75) // The host unit alone maps this code to graceful guest stop + poweroff.
	default:
	}
}
