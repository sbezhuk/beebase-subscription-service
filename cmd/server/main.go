// Command server is the entry point for the BeeBase subscription-service.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/joho/godotenv"

	appsub "github.com/sbezhuk/beebase-subscription-service/internal/application/subscription"
	"github.com/sbezhuk/beebase-subscription-service/internal/config"
	"github.com/sbezhuk/beebase-subscription-service/internal/domain/subscription"
	"github.com/sbezhuk/beebase-subscription-service/internal/platform/apple"
	"github.com/sbezhuk/beebase-subscription-service/internal/platform/google"
	"github.com/sbezhuk/beebase-subscription-service/internal/platform/postgres"
	repopostgres "github.com/sbezhuk/beebase-subscription-service/internal/repository/postgres"
	transporthttp "github.com/sbezhuk/beebase-subscription-service/internal/transport/http"
	subhttp "github.com/sbezhuk/beebase-subscription-service/internal/transport/http/subscription"
	webhookhttp "github.com/sbezhuk/beebase-subscription-service/internal/transport/http/webhook"

	"github.com/sbezhuk/beebase-common/authmw"
	"github.com/sbezhuk/beebase-common/logger"
	"github.com/sbezhuk/beebase-common/server"
	"github.com/sbezhuk/beebase-common/sessionstore"
)

func main() {
	if err := run(); err != nil {
		slog.Error("server exited with error", "error", err)
		os.Exit(1)
	}
}

func run() error {
	// .env is optional: present in local dev, absent in production/containers.
	_ = godotenv.Load()

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	log := logger.New(cfg.Env, cfg.LogLevel)
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	connectCtx, cancelConnect := context.WithTimeout(ctx, cfg.DatabaseConnectTimeout)
	db, err := postgres.New(connectCtx, cfg.DatabaseURL)
	cancelConnect()
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}
	defer db.Close()

	log.Info("connected to database")

	redisConnectCtx, cancelRedisConnect := context.WithTimeout(ctx, cfg.RedisConnectTimeout)
	redisClient, err := sessionstore.NewRedisClient(redisConnectCtx, cfg.RedisAddr)
	cancelRedisConnect()
	if err != nil {
		return fmt.Errorf("connect to redis: %w", err)
	}
	defer redisClient.Close()

	log.Info("connected to redis")

	sessions := sessionstore.NewStore(redisClient)

	verifier, err := authmw.NewVerifierFromJWKSURL(ctx, cfg.AuthJWKSURL, sessions)
	if err != nil {
		return fmt.Errorf("build JWKS verifier: %w", err)
	}

	repo := repopostgres.NewSubscriptionRepository(db)
	appleVerifier := apple.NewDefaultVerifier(nil)
	expectedEnv := subscription.EnvironmentProduction
	if strings.EqualFold(cfg.AppleEnvironment, "sandbox") {
		expectedEnv = subscription.EnvironmentSandbox
	}
	appService := appsub.NewService(repo, appleVerifier, cfg.AppleBundleID, expectedEnv, log)
	appleHandler := webhookhttp.NewAppleHandler(appService, log)

	var googleHandler *webhookhttp.GoogleHandler
	googleClient, err := google.NewClient(ctx, google.Config{
		ServiceAccountJSON: cfg.GoogleServiceAccountJSON,
		PackageName:        cfg.GooglePackageName,
	})
	if err != nil {
		log.Warn("could not initialize google client, google webhook disabled", "error", err)
	} else {
		appService.WithGoogle(googleClient, cfg.GooglePackageName)
		googleHandler = webhookhttp.NewGoogleHandler(appService, log)
	}

	subscriptionHandler := subhttp.NewHandler(appService, log)

	router := transporthttp.NewRouter(log, db, subscriptionHandler, appleHandler, googleHandler, verifier, cfg.InternalServiceToken)

	srv := server.New(server.Config{
		Addr:         ":" + cfg.HTTPPort,
		Handler:      router,
		ReadTimeout:  cfg.HTTPReadTimeout,
		WriteTimeout: cfg.HTTPWriteTimeout,
		IdleTimeout:  cfg.HTTPIdleTimeout,
	})

	errCh := make(chan error, 1)
	go func() {
		log.Info("starting http server", "port", cfg.HTTPPort, "env", cfg.Env)
		errCh <- srv.Run()
	}()

	select {
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("run server: %w", err)
		}
		return nil
	case <-ctx.Done():
		log.Info("shutdown signal received")
	}

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), cfg.HTTPShutdownTimeout)
	defer cancelShutdown()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}

	log.Info("server stopped cleanly")
	return nil
}
