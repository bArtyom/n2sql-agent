// Command retrieval-eval compares retrieval thresholds against labeled questions.
// It only calls the embedding/search path in --live mode; it never calls chat.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/bArtyom/n2sql-agent/internal/config"
	"github.com/bArtyom/n2sql-agent/internal/documentchunk"
	"github.com/bArtyom/n2sql-agent/internal/modelclient"
	"github.com/bArtyom/n2sql-agent/internal/modelprovider"
	"github.com/bArtyom/n2sql-agent/internal/modelruntime"
	"github.com/bArtyom/n2sql-agent/internal/retrieval"
	"github.com/bArtyom/n2sql-agent/internal/retrievaleval"
	_ "github.com/jackc/pgx/v5/stdlib"
)

const (
	defaultCaseFile   = "eval/retrieval-threshold-cases.json"
	defaultMaxCases   = 20
	defaultThresholds = "0.55,0.60,0.65,0.70,0.75"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "retrieval eval:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if ctx == nil {
		return errors.New("evaluation context is required")
	}
	flags := flag.NewFlagSet("retrieval-eval", flag.ContinueOnError)
	flags.SetOutput(stderr)
	casePath := flags.String("cases", defaultCaseFile, "JSON retrieval evaluation case file")
	thresholdText := flags.String("thresholds", defaultThresholds, "comma-separated cosine distance thresholds")
	maxCases := flags.Int("max-cases", defaultMaxCases, "maximum number of cases to execute")
	live := flags.Bool("live", false, "call the configured embedding service and database; this consumes embedding quota")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	thresholds, err := parseThresholds(*thresholdText)
	if err != nil {
		return err
	}
	caseFile, err := os.Open(*casePath)
	if err != nil {
		return fmt.Errorf("open cases file: %w", err)
	}
	defer caseFile.Close()
	cases, err := retrievaleval.LoadCases(caseFile)
	if err != nil {
		return fmt.Errorf("load cases: %w", err)
	}
	cases, err = retrievaleval.LimitCases(cases, *maxCases)
	if err != nil {
		return fmt.Errorf("limit cases: %w", err)
	}

	if !*live {
		fmt.Fprintf(stderr, "dry-run: validated %d case(s), no embedding or database calls\n", len(cases))
		return json.NewEncoder(stdout).Encode(map[string]any{
			"live":       false,
			"case_count": len(cases),
			"thresholds": thresholds,
		})
	}

	searcher, closeSearcher, err := newLiveSearcher(ctx)
	if err != nil {
		return err
	}
	defer closeSearcher()
	fmt.Fprintf(stderr, "live retrieval evaluation: %d case(s), embedding quota may be consumed\n", len(cases))
	report, err := retrievaleval.Evaluate(ctx, searcher, cases, thresholds)
	if err != nil {
		return fmt.Errorf("evaluate retrieval: %w", err)
	}
	return json.NewEncoder(stdout).Encode(report)
}

func parseThresholds(value string) ([]float64, error) {
	parts := strings.Split(value, ",")
	thresholds := make([]float64, 0, len(parts))
	for _, part := range parts {
		threshold, err := strconv.ParseFloat(strings.TrimSpace(part), 64)
		if err != nil {
			return nil, fmt.Errorf("parse threshold %q: %w", part, err)
		}
		thresholds = append(thresholds, threshold)
	}
	if err := retrievaleval.ValidateThresholds(thresholds); err != nil {
		return nil, err
	}
	return thresholds, nil
}

func newLiveSearcher(ctx context.Context) (retrieval.Searcher, func(), error) {
	cfg := config.Load()
	if cfg.DatabaseURL == "" {
		return nil, func() {}, errors.New("DATABASE_URL is required for --live")
	}
	db, err := sql.Open("pgx", cfg.DatabaseURL)
	if err != nil {
		return nil, func() {}, fmt.Errorf("open database: %w", err)
	}
	closeDB := func() { _ = db.Close() }
	if err := db.PingContext(ctx); err != nil {
		closeDB()
		return nil, func() {}, fmt.Errorf("connect database: %w", err)
	}
	providerStore := modelprovider.NewPostgresStore(db)
	client := modelclient.NewHTTPClient(&http.Client{Timeout: 20 * time.Second}, cfg.ModelProviderAllowedHosts)
	embeddings := modelruntime.NewEmbeddingService(providerStore, client, cfg.ModelProviderAPIKeyEnvVar, os.LookupEnv)
	return retrieval.NewService(embeddings, documentchunk.NewPostgresStore(db)), closeDB, nil
}
