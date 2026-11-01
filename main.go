// Command go2rtc-mcp is a read-only MCP server that exposes go2rtc camera snapshots.
package main

import (
	"context"
	"crypto/subtle"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tfielding/go2rtc-mcp/internal/go2rtc"
	"github.com/tfielding/go2rtc-mcp/internal/tools"
)

// version is set at build time: -ldflags "-X main.version=v1.2.3".
var version = "dev"

type config struct {
	go2rtcURL, go2rtcUser, go2rtcPass string
	listen                            string
	timeout                           time.Duration
	token                             string
	opt                               tools.Options
}

func loadConfig() (config, error) {
	c := config{
		go2rtcURL:  os.Getenv("GO2RTC_URL"),
		go2rtcUser: os.Getenv("GO2RTC_USERNAME"),
		go2rtcPass: os.Getenv("GO2RTC_PASSWORD"),
		listen:     env("LISTEN_ADDR", ":8090"),
		token:      os.Getenv("AUTH_TOKEN"),
		opt:        tools.Options{Allowed: map[string]bool{}},
	}
	if c.go2rtcURL == "" {
		return c, errors.New("GO2RTC_URL is required (e.g. http://go2rtc:1984)")
	}
	for _, name := range strings.Split(os.Getenv("CAMERAS"), ",") {
		if name = strings.TrimSpace(name); name != "" {
			c.opt.Allowed[strings.ToLower(name)] = true
		}
	}
	for _, pat := range strings.Split(os.Getenv("EXCLUDE_CAMERAS"), ",") {
		if pat = strings.ToLower(strings.TrimSpace(pat)); pat != "" {
			if _, err := path.Match(pat, ""); err != nil {
				return c, fmt.Errorf("EXCLUDE_CAMERAS: bad pattern %q", pat)
			}
			c.opt.Exclude = append(c.opt.Exclude, pat)
		}
	}
	var err error
	if c.opt.DefaultWidth, err = strconv.Atoi(env("SNAPSHOT_WIDTH", "1280")); err != nil || c.opt.DefaultWidth < 0 {
		return c, errors.New("SNAPSHOT_WIDTH must be a non-negative integer (0 = native size)")
	}
	if c.opt.MaxWidth, err = strconv.Atoi(env("MAX_WIDTH", "1920")); err != nil || c.opt.MaxWidth < 0 {
		return c, errors.New("MAX_WIDTH must be a non-negative integer (0 = no cap)")
	}
	if c.timeout, err = duration("GO2RTC_TIMEOUT", "20s"); err != nil || c.timeout <= 0 {
		return c, errors.New("GO2RTC_TIMEOUT must be a positive duration, e.g. 20s or 1m (bare numbers are seconds)")
	}
	if c.opt.CacheTTL, err = duration("SNAPSHOT_CACHE", "2s"); err != nil || c.opt.CacheTTL < 0 {
		return c, errors.New("SNAPSHOT_CACHE must be a non-negative duration, e.g. 2s (0 = no cache; bare numbers are seconds)")
	}
	return c, nil
}

func main() {
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: go2rtc-mcp [-version]\n\nConfigured with environment variables; see the README.\n")
	}
	flag.Parse()
	if *showVersion {
		fmt.Println("go2rtc-mcp", version)
		return
	}
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	src := go2rtc.New(cfg.go2rtcURL, cfg.go2rtcUser, cfg.go2rtcPass, cfg.timeout)
	srv := &http.Server{Addr: cfg.listen, Handler: newHandler(src, cfg), ReadHeaderTimeout: 10 * time.Second}

	allowed := "all"
	if len(cfg.opt.Allowed) > 0 {
		allowed = fmt.Sprint(len(cfg.opt.Allowed))
	}
	slog.Info("starting", "version", version, "listen", cfg.listen, "go2rtc", cfg.go2rtcURL,
		"cameras", allowed, "exclude", strings.Join(cfg.opt.Exclude, ","), "auth", cfg.token != "", "timeout", cfg.timeout, "cache", cfg.opt.CacheTTL)
	if err := src.Ping(ctx); err != nil {
		slog.Warn("go2rtc not reachable yet", "err", err)
	}

	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
	}()
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	slog.Info("shut down")
	return nil
}

func newHandler(src *go2rtc.Client, cfg config) http.Handler {
	server := mcp.NewServer(&mcp.Implementation{Name: "go2rtc-mcp", Version: version}, nil)
	tools.Register(server, src, cfg.opt)
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)

	mux := http.NewServeMux()
	mux.Handle("/mcp", requireToken(cfg.token, mcpHandler))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := src.Ping(r.Context()); err != nil {
			http.Error(w, "go2rtc unreachable", http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte("ok\n"))
	})
	return mux
}

// requireToken enforces "Authorization: Bearer <token>" when a token is configured.
func requireToken(token string, next http.Handler) http.Handler {
	if token == "" {
		return next
	}
	want := []byte("Bearer " + token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// duration reads a Go duration ("20s", "1m"); a bare number is taken as seconds.
func duration(key, def string) (time.Duration, error) {
	v := env(key, def)
	if n, err := strconv.ParseFloat(v, 64); err == nil {
		return time.Duration(n * float64(time.Second)), nil
	}
	return time.ParseDuration(v)
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
