package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	av1 "github.com/imbpp123/market-analyzer/api/go/marketanalyzer/v1"
	dv1 "github.com/imbpp123/market-data/api/go/marketdata/v1"
	"github.com/imbpp123/market-mcp/internal/adapter"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type config struct {
	transport    string
	httpAddr     string
	dataAddr     string
	analyzerAddr string
	timeout      time.Duration
	maxBytes     int
}

func env(key, fallback string) string {
	if s := os.Getenv(key); s != "" {
		return s
	}
	return fallback
}

func loadConfig() (config, error) {
	c := config{
		transport:    env("MARKET_MCP_TRANSPORT", "stdio"),
		httpAddr:     env("MARKET_MCP_HTTP_ADDR", "127.0.0.1:8082"),
		dataAddr:     env("MARKET_DATA_ENDPOINT", "127.0.0.1:9090"),
		analyzerAddr: env("MARKET_ANALYZER_ENDPOINT", "127.0.0.1:9091"),
	}
	var err error
	c.timeout, err = time.ParseDuration(env("MARKET_MCP_TIMEOUT", "30s"))
	if err != nil || c.timeout <= 0 {
		return c, errors.New("MARKET_MCP_TIMEOUT must be a positive duration")
	}
	c.maxBytes, err = strconv.Atoi(env("MARKET_MCP_MAX_RESULT_BYTES", strconv.Itoa(adapter.DefaultMaxResultBytes)))
	if err != nil || c.maxBytes < 1024 || c.maxBytes > 4<<20 {
		return c, errors.New("MARKET_MCP_MAX_RESULT_BYTES must be between 1024 and 4194304")
	}
	if c.transport != "stdio" && c.transport != "http" {
		return c, errors.New("MARKET_MCP_TRANSPORT must be stdio or http")
	}
	for _, addr := range []string{c.httpAddr, c.dataAddr, c.analyzerAddr} {
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			return c, fmt.Errorf("invalid endpoint %q: %w", addr, err)
		}
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return c, fmt.Errorf("endpoint %q must use a loopback IP address", addr)
		}
	}
	return c, nil
}

func run(ctx context.Context, c config) error {
	dataConn, err := grpc.NewClient(c.dataAddr, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(16<<20)))
	if err != nil {
		return fmt.Errorf("data client: %w", err)
	}
	defer dataConn.Close()
	analyzerConn, err := grpc.NewClient(c.analyzerAddr, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(32<<20)))
	if err != nil {
		return fmt.Errorf("analyzer client: %w", err)
	}
	defer analyzerConn.Close()

	a, err := adapter.New(dv1.NewMarketDataServiceClient(dataConn), av1.NewMarketAnalyzerServiceClient(analyzerConn), c.timeout, c.maxBytes)
	if err != nil {
		return err
	}
	s := a.Server()
	if c.transport == "stdio" {
		return s.Run(ctx, &mcp.StdioTransport{})
	}

	listener, err := net.Listen("tcp", c.httpAddr)
	if err != nil {
		return err
	}
	httpServer := &http.Server{Handler: httpHandler(s), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 2 * time.Minute}
	serveErr := make(chan error, 1)
	go func() { serveErr <- httpServer.Serve(listener) }()
	slog.Info("MCP HTTP listening", "address", listener.Addr().String())
	select {
	case err = <-serveErr:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			_ = httpServer.Close()
			return err
		}
		if err := <-serveErr; !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	return nil
}

func httpHandler(s *mcp.Server) http.Handler {
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, &mcp.StreamableHTTPOptions{SessionTimeout: 5 * time.Minute})
	mux := http.NewServeMux()
	mux.Handle("/mcp", http.NewCrossOriginProtection().Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		handler.ServeHTTP(w, r)
	})))
	return mux
}

func main() {
	c, err := loadConfig()
	if err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, c); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("service stopped", "error", err)
		os.Exit(1)
	}
}
