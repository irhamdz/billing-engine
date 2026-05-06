// Command api runs the billing-engine HTTP server.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	httpapi "github.com/irhamdz/billing-engine/internal/http"
	"github.com/irhamdz/billing-engine/internal/repository/sqlite"
	"github.com/irhamdz/billing-engine/internal/service"
)

func main() {
	dbPath := flag.String("db", "billing.db", "sqlite database file")
	addr := flag.String("addr", ":8080", "listen address")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	db, err := sqlite.OpenDB(ctx, *dbPath)
	if err != nil {
		logger.Error("open db", "err", err)
		os.Exit(1)
	}
	defer db.Close()

	loanRepo := sqlite.NewLoanRepository(db)
	pmtRepo := sqlite.NewPaymentRepository(db)
	svc := service.NewBillingService(db, loanRepo, pmtRepo)
	router := httpapi.NewRouterWithLogger(svc, logger)

	srv := &http.Server{
		Addr:              *addr,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		logger.Info("listening", "addr", *addr, "db", *dbPath)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("listen", "err", err)
			cancel()
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown", "err", err)
	}
}
