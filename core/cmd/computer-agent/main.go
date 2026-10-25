package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"nori/internal/computerd"
)

func main() {
	listen := flag.String("listen", "172.30.0.2:8090", "HTTP bind address")
	proxy := flag.Bool("proxy", false, "forward RPC to the guest instead of running tools")
	tasks := flag.String("tasks", "/workspace/tasks", "task working directory")
	flag.Parse()
	var handler http.Handler
	if *proxy {
		handler = computerd.Proxy()
	} else {
		var err error
		handler, err = computerd.New(*tasks)
		if err != nil {
			log.Fatal(err)
		}
	}
	server := &http.Server{Addr: *listen, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 125 * time.Second, MaxHeaderBytes: 8192}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		deadline, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(deadline)
	}()
	log.Printf("computer RPC listening on %s", *listen)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
