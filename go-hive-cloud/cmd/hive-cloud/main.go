// Command hive-cloud is a private-deploy replacement for the client-observable
// surface of hive-backend: self-signed JWTs, per-team SQLite global DB, inbox
// SSE, gateway availability and stream-token refresh.
//
//	hive-cloud serve [flags]   # run the HTTP+SSE service
//	hive-cloud token [flags]   # mint an operator JWT or a streamToken
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/shark2202/go_hive/go-hive-cloud/internal/auth"
	"github.com/shark2202/go_hive/go-hive-cloud/internal/config"
	"github.com/shark2202/go_hive/go-hive-cloud/internal/httpapi"
)

const version = "0.1.0-m1"

func main() {
	os.Exit(run(os.Args[1:]))
}

func usage() {
	fmt.Fprint(os.Stderr, `hive-cloud `+version+`

Usage:
  hive-cloud serve [flags]      run the HTTP + SSE service
  hive-cloud token [flags]      mint an operator JWT or a streamToken
  hive-cloud version            print the version

Run "hive-cloud <command> -h" for command flags.
`)
}

func run(args []string) int {
	if len(args) == 0 {
		usage()
		return 2
	}
	switch args[0] {
	case "serve":
		return runServe(args[1:])
	case "token":
		return runToken(args[1:])
	case "version", "-version", "--version":
		fmt.Println("hive-cloud " + version)
		return 0
	case "help", "-h", "--help":
		usage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "hive-cloud: unknown command %q\n\n", args[0])
		usage()
		return 2
	}
}

// ---- serve ----------------------------------------------------------------

func runServe(args []string) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	listen := fs.String("listen", "", "listen address (default 127.0.0.1:8788; :0 picks a free port)")
	dataDir := fs.String("data-dir", "", "data directory (default ./data)")
	configPath := fs.String("config", "", "config.json path (0600)")
	jwtSecret := fs.String("jwt-secret", "", "operator JWT signing secret (default: <data-dir>/jwt_secret)")
	streamSecret := fs.String("stream-secret", "", "streamToken signing secret (default: <data-dir>/stream_secret)")
	statementTimeout := fs.Duration("statement-timeout", 0, "per-statement SQL timeout (default 5s)")
	rowCap := fs.Int("row-cap", 0, "default row cap for SQL results, 0 = unlimited (default 1000)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	cfg := config.Defaults()
	if *configPath != "" {
		file, err := config.ReadFile(*configPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "hive-cloud: %v\n", err)
			return 1
		}
		cfg = cfg.Apply(file)
	}
	cfg = cfg.ApplyEnv()
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if set["listen"] {
		cfg.Listen = *listen
	}
	if set["data-dir"] {
		cfg.DataDir = *dataDir
	}
	if set["jwt-secret"] {
		cfg.JWTSecret = []byte(*jwtSecret)
	}
	if set["stream-secret"] {
		cfg.StreamSecret = []byte(*streamSecret)
	}
	if set["statement-timeout"] {
		cfg.StatementTimeout = *statementTimeout
	}
	if set["row-cap"] {
		cfg.RowCapDefault = *rowCap
	}

	resolved, err := config.ResolveSecrets(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hive-cloud: %v\n", err)
		return 1
	}
	logger := log.New(os.Stderr, "", log.LstdFlags)
	server, err := httpapi.New(httpapi.Config{
		DataDir:          resolved.DataDir,
		JWTSecret:        resolved.JWTSecret,
		StreamSecrets:    [][]byte{resolved.StreamSecret},
		StatementTimeout: resolved.StatementTimeout,
		RowCapDefault:    resolved.RowCapDefault,
		Version:          version,
		Logger:           logger,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "hive-cloud: %v\n", err)
		return 1
	}
	defer server.Close()

	listener, err := net.Listen("tcp", resolved.Listen)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hive-cloud: listen %s: %v\n", resolved.Listen, err)
		return 1
	}
	httpServer := &http.Server{
		Handler:           server.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// No WriteTimeout: the inbox SSE stream is long-lived by contract.
	}
	// Machine-readable readiness line (scripts/smoke.sh parses it).
	fmt.Printf("hive-cloud: listening on http://%s (data_dir=%s)\n", listener.Addr().String(), resolved.DataDir)
	logger.Printf("[cloud] serving %s (data_dir=%s version=%s)", listener.Addr().String(), resolved.DataDir, version)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan struct{})
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
		close(done)
	}()
	if err := httpServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintf(os.Stderr, "hive-cloud: serve: %v\n", err)
		return 1
	}
	<-done
	return 0
}

// ---- token ----------------------------------------------------------------

func runToken(args []string) int {
	fs := flag.NewFlagSet("token", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dataDir := fs.String("data-dir", "", "data directory (default ./data)")
	jwtSecret := fs.String("jwt-secret", "", "operator JWT signing secret")
	streamSecret := fs.String("stream-secret", "", "streamToken signing secret")
	userID := fs.String("user-id", "", "user id (sub/user_id claim) — required")
	teamID := fs.String("team-id", "", "team id claim — required")
	ttl := fs.Duration("ttl", 24*time.Hour, "token lifetime")
	tokenType := fs.String("type", "user", "user|stream")
	asJSON := fs.Bool("json", false, "print JSON {token,type,exp,user_id,team_id}")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *userID == "" || *teamID == "" {
		fmt.Fprintln(os.Stderr, "hive-cloud token: --user-id and --team-id are required")
		return 2
	}
	if !auth.ValidTeamID(*teamID) {
		fmt.Fprintf(os.Stderr, "hive-cloud token: invalid team id %q (allowed: letters, digits, '-', '_', '.')\n", *teamID)
		return 2
	}
	if *tokenType != "user" && *tokenType != "stream" {
		fmt.Fprintf(os.Stderr, "hive-cloud token: --type must be user or stream\n")
		return 2
	}

	cfg := config.Defaults()
	cfg = cfg.ApplyEnv()
	if *dataDir != "" {
		cfg.DataDir = *dataDir
	}
	if *jwtSecret != "" {
		cfg.JWTSecret = []byte(*jwtSecret)
	}
	if *streamSecret != "" {
		cfg.StreamSecret = []byte(*streamSecret)
	}
	resolved, err := config.ResolveSecrets(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hive-cloud: %v\n", err)
		return 1
	}

	now := time.Now()
	var secret []byte
	var token string
	if *tokenType == "stream" {
		secret = resolved.StreamSecret
		token, err = auth.NewSigner(secret, nil).MintStreamToken(*userID, *teamID, *ttl)
	} else {
		secret = resolved.JWTSecret
		token, err = auth.NewSigner(secret, nil).MintUserToken(*userID, *teamID, *ttl)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "hive-cloud token: %v\n", err)
		return 1
	}
	exp := now.Add(*ttl).Unix()
	if *asJSON {
		payload, _ := json.Marshal(map[string]any{
			"token":   token,
			"type":    *tokenType,
			"exp":     exp,
			"user_id": *userID,
			"team_id": *teamID,
		})
		fmt.Println(string(payload))
		return 0
	}
	fmt.Println(token)
	return 0
}

var _ = strings.TrimSpace
