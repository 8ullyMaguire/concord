package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"git.polarisocial.xyz/concord/concord/internal/config"
	"git.polarisocial.xyz/concord/concord/internal/db"
	"git.polarisocial.xyz/concord/concord/internal/httpapi"
	"git.polarisocial.xyz/concord/concord/internal/store"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	var (
		flagDB     = flag.String("db", "", "database path (overrides $CONCORD_DB)")
		flagListen = flag.String("listen", "", "listen address (overrides $CONCORD_LISTEN)")
		showVer    = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()

	if *showVer {
		fmt.Println(version)
		return
	}

	cfg := config.Load()
	if *flagDB != "" {
		cfg.DBPath = *flagDB
	}
	if *flagListen != "" {
		cfg.Listen = *flagListen
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	sqlDB, err := db.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("open db %s: %v", cfg.DBPath, err)
	}
	defer sqlDB.Close()

	if err := db.Migrate(ctx, sqlDB); err != nil {
		log.Fatalf("migrate: %v", err)
	}

	srv := &httpapi.Server{
		Store:   &store.DB{DB: sqlDB},
		Version: version,
	}

	httpSrv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.Router(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() { errCh <- httpSrv.ListenAndServe() }()
	log.Printf("concord %s listening on %s (db: %s)", version, cfg.Listen, cfg.DBPath)

	select {
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			log.Fatalf("serve: %v", err)
		}
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Printf("graceful shutdown failed: %v", err)
	}
}
