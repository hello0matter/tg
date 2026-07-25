package main

import (
	"context"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"tgworkbench/internal/connector"
	"tgworkbench/internal/runtime"
	"tgworkbench/internal/server"
	"tgworkbench/internal/store"
	"tgworkbench/internal/vault"
	"tgworkbench/webui"
)

func main() {
	dataDirFlag := flag.String("data", "", "data directory")
	listenFlag := flag.String("listen", "", "listen address")
	flag.Parse()

	dataDir := *dataDirFlag
	if dataDir == "" {
		dataDir = defaultDataDir()
	}
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	db, err := store.Open(filepath.Join(dataDir, "workbench.db"))
	fatalIf(log, err)
	defer db.Close()
	secure, err := vault.Open(dataDir)
	fatalIf(log, err)
	settings, err := db.Settings()
	fatalIf(log, err)
	listen := settings.ListenAddress
	if *listenFlag != "" {
		listen = *listenFlag
	}
	if listen == "" {
		listen = "127.0.0.1:8765"
	}
	if err := validateListenAddress(listen); err != nil {
		fatalIf(log, err)
	}
	embedded, _ := webui.Files()
	assets, err := fs.Sub(embedded, "dist")
	fatalIf(log, err)
	tgRuntime := runtime.NewManager(dataDir, db, secure, log)
	defer tgRuntime.Close()
	connectors := connector.NewRegistry(db)
	fatalIf(log, connectors.Register(tgRuntime))
	app := server.New(db, secure, connectors, assets, log)
	accounts, err := db.ListAccounts()
	fatalIf(log, err)
	for _, account := range accounts {
		if err := connectors.Connect(account.ID); err != nil {
			log.Warn("restore Telegram account", "account", account.Name, "error", err)
		}
	}
	httpServer := &http.Server{Addr: listen, Handler: app.Handler(), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	go func() {
		log.Info("TG Workbench started", "url", "http://"+listen, "data", dataDir)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("server stopped", "error", err)
			os.Exit(1)
		}
	}()
	if settings.OpenBrowser {
		go func() {
			time.Sleep(400 * time.Millisecond)
			if err := exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", "http://"+listen).Start(); err != nil {
				log.Warn("open browser", "error", err)
			}
		}()
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(shutdown)
}

func defaultDataDir() string {
	if local := os.Getenv("LOCALAPPDATA"); local != "" {
		return filepath.Join(local, "TGWorkbench")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".tg-workbench"
	}
	return filepath.Join(home, ".tg-workbench")
}
func fatalIf(log *slog.Logger, err error) {
	if err != nil {
		log.Error("startup failed", "error", fmt.Sprint(err))
		os.Exit(1)
	}
}

func validateListenAddress(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid listen address %q: %w", address, err)
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if !strings.EqualFold(host, "localhost") && (ip == nil || !ip.IsLoopback()) {
		return fmt.Errorf("listen address must be loopback, got %q", host)
	}
	return nil
}
