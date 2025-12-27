package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	"uplink/backend/internal/api"
	"uplink/backend/internal/config"
	"uplink/backend/internal/db"
	"uplink/backend/internal/game"
)

func main() {
	cfg := config.Load()
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	store, err := db.New(cfg.DatabaseURL, cfg.DBMaxConns)
	if err != nil {
		log.Error("ошибка инициализации бд", "err", err)
		os.Exit(1)
	}
	defer store.Close()

	gm := game.New(store, log)
	srv := &http.Server{
		Addr:         cfg.Port,
		Handler:      api.New(store, gm, cfg.JWTSecret, cfg.AllowedOrigins, log),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		log.Info("сервер запущен", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("ошибка сервера", "err", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	gm.Shutdown()
	if err := srv.Shutdown(ctx); err != nil {
		log.Error("ошибка остановки", "err", err)
	}
}
