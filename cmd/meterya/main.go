package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"meterya.local/api/internal/auth"
	"meterya.local/api/internal/httpapi"
	"meterya.local/api/internal/store"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		return fmt.Errorf("DATABASE_URL is required")
	}
	s, err := store.Open(ctx, url)
	if err != nil {
		return err
	}
	defer s.Pool.Close()
	mode := "serve"
	if len(os.Args) > 1 {
		mode = os.Args[1]
	}
	if mode == "bootstrap" {
		f := flag.NewFlagSet(mode, flag.ContinueOnError)
		name := f.String("name", "Haushalt", "Workspace name")
		sub := f.String("subject", "", "Verified Keycloak subject")
		email := f.String("email", "", "Admin email")
		if err = f.Parse(os.Args[2:]); err != nil {
			return err
		}
		if *sub == "" || *email == "" {
			return fmt.Errorf("subject and email are required")
		}
		id, err := s.Bootstrap(ctx, *name, *sub, *email)
		if err == nil {
			fmt.Println(id)
		}
		return err
	}
	if mode == "rebuild" {
		return s.Rebuild(ctx)
	}
	if mode == "import-monthly" {
		return importMonthly(ctx, s)
	}
	if mode != "serve" {
		return fmt.Errorf("unknown command")
	}
	origin := env("PUBLIC_ORIGIN", "http://localhost:8080")
	issuer := env("OIDC_ISSUER", origin+"/identity/realms/meterya")
	internal := env("OIDC_INTERNAL_BASE", origin+"/identity")
	client, err := auth.New(ctx, s, origin, issuer, internal, "meterya-web", os.Getenv("OIDC_CLIENT_SECRET"))
	if err != nil {
		return err
	}
	server := httpapi.Server{Store: s, Auth: client, Identity: auth.Keycloak{Base: internal, Realm: "meterya", ClientID: "meterya-invitations", Secret: os.Getenv("KEYCLOAK_SERVICE_SECRET"), Client: &http.Client{Timeout: 20 * time.Second}}}
	server.OperatorSubject = os.Getenv("OPERATOR_SUBJECT")
	go func() {
		ticker := time.NewTicker(15 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cleanup, cancel := context.WithTimeout(ctx, 30*time.Second)
				_ = s.SweepPhotoDrafts(cleanup)
				cancel()
			}
		}
	}()
	httpServer := &http.Server{Addr: env("LISTEN_ADDR", ":8081"), Handler: server.Handler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 60 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdown)
	}()
	slog.Info("Meterya API ready", "address", httpServer.Addr)
	if err = httpServer.ListenAndServe(); err == http.ErrServerClosed {
		return nil
	}
	return err
}
func main() {
	if err := run(); err != nil {
		slog.Error("Meterya stopped", "error", err)
		os.Exit(1)
	}
}
