package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"deskpatrol/internal/appconfig"
	"deskpatrol/internal/database"
	"deskpatrol/internal/meshcentral"
	"deskpatrol/internal/server"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "migrate" {
		if err := migrate(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	configPath := flag.String("config", appconfig.Path(), "配置文件路径")
	listen := flag.String("listen", "127.0.0.1:18123", "监听地址")
	assets := flag.String("assets", "frontend/apps/admin/dist", "管理端静态资源目录")
	logPath := flag.String("log", "var/log/server.log", "日志文件路径")
	pidPath := flag.String("pid", "var/run/server.pid", "PID 文件路径")
	flag.Parse()

	logger, closeLog, err := newLogger(filepath.Clean(*logPath))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer closeLog()

	app, err := server.New(filepath.Clean(*configPath), filepath.Clean(*assets), logger)
	if err != nil {
		logger.Error("server initialization failed", "error", err)
		os.Exit(1)
	}
	defer app.Close()

	if err := writePID(filepath.Clean(*pidPath)); err != nil {
		logger.Error("pid file write failed", "error", err)
		os.Exit(1)
	}
	defer os.Remove(filepath.Clean(*pidPath))

	httpServer := &http.Server{Addr: *listen, Handler: app.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 2 * time.Minute, IdleTimeout: 2 * time.Minute}
	serverError := make(chan error, 1)
	go func() {
		logger.Info("deskpatrol server ready", "listen", *listen, "config", *configPath)
		serverError <- httpServer.ListenAndServe()
	}()

	signalContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	select {
	case <-signalContext.Done():
		logger.Info("server shutdown requested")
	case err := <-serverError:
		if !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server stopped unexpectedly", "error", err)
			os.Exit(1)
		}
		return
	}
	shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownContext); err != nil {
		logger.Error("server graceful shutdown failed", "error", err)
		os.Exit(1)
	}
}

func migrate(args []string) error {
	flags := flag.NewFlagSet("migrate", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", appconfig.Path(), "配置文件路径")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("迁移参数不正确: %w", err)
	}
	if flags.NArg() != 0 {
		return errors.New("迁移命令包含多余参数")
	}
	cfg, err := appconfig.Load(filepath.Clean(*configPath))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := database.Open(ctx, cfg.Database)
	if err != nil {
		return err
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, database.Schema); err != nil {
		return fmt.Errorf("执行数据库迁移失败: %w", err)
	}
	if err := meshcentral.WriteDeploymentFiles(filepath.Dir(filepath.Clean(*configPath)), cfg); err != nil {
		return fmt.Errorf("更新 MeshCentral 运行配置失败: %w", err)
	}
	fmt.Println("DeskPatrol 数据库迁移完成")
	return nil
}

func newLogger(path string) (*slog.Logger, func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, nil, fmt.Errorf("创建日志目录失败: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, nil, fmt.Errorf("打开日志文件失败: %w", err)
	}
	writer := io.MultiWriter(os.Stdout, file)
	return slog.New(slog.NewJSONHandler(writer, &slog.HandlerOptions{Level: slog.LevelInfo})), func() { _ = file.Close() }, nil
}

func writePID(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o600)
}
