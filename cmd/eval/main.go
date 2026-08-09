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
	"syscall"
	"time"

	"github.com/bArtyom/n2sql-agent/internal/agent"
	"github.com/bArtyom/n2sql-agent/internal/agenteval"
	"github.com/bArtyom/n2sql-agent/internal/agentservice"
	"github.com/bArtyom/n2sql-agent/internal/config"
	"github.com/bArtyom/n2sql-agent/internal/documentchunk"
	"github.com/bArtyom/n2sql-agent/internal/modelclient"
	"github.com/bArtyom/n2sql-agent/internal/modelprovider"
	"github.com/bArtyom/n2sql-agent/internal/modelruntime"
	"github.com/bArtyom/n2sql-agent/internal/retrieval"
	_ "github.com/jackc/pgx/v5/stdlib"
)

const defaultCaseFile = "eval/cases.example.json"
const defaultMaxCases = 5

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "agent eval:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if ctx == nil {
		return errors.New("evaluation context is required")
	}
	flags := flag.NewFlagSet("eval", flag.ContinueOnError)
	flags.SetOutput(stderr)
	casePath := flags.String("cases", defaultCaseFile, "JSON evaluation case file")
	live := flags.Bool("live", false, "call the configured model and database; this consumes API quota")
	dryRun := flags.Bool("dry-run", false, "run with deterministic local responses without API calls")
	maxCases := flags.Int("max-cases", defaultMaxCases, "maximum number of cases to execute")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *live && *dryRun {
		return errors.New("--live and --dry-run cannot be used together")
	}

	caseFile, err := os.Open(*casePath)
	if err != nil {
		return fmt.Errorf("open cases file: %w", err)
	}
	defer caseFile.Close()
	cases, err := agenteval.LoadCases(caseFile)
	if err != nil {
		return fmt.Errorf("load cases: %w", err)
	}
	cases, err = agenteval.LimitCases(cases, *maxCases)
	if err != nil {
		return fmt.Errorf("limit cases: %w", err)
	}

	var answerer agentservice.Answerer
	var closeAnswerer func()
	if *live {
		answerer, closeAnswerer, err = newLiveAnswerer(ctx)
		if err != nil {
			return err
		}
		defer closeAnswerer()
		fmt.Fprintf(stderr, "running %d live evaluation case(s); API quota may be consumed\n", len(cases))
	} else {
		answerer = dryRunAnswerer{}
		fmt.Fprintf(stderr, "running %d dry-run evaluation case(s); no model or database calls\n", len(cases))
	}

	report, err := agenteval.Evaluate(ctx, answerer, cases)
	if err != nil {
		return fmt.Errorf("evaluate cases: %w", err)
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(report)
}

type dryRunAnswerer struct{}

func (dryRunAnswerer) Answer(ctx context.Context, _ int64, _ agentservice.ChatRequest) (agentservice.Response, error) {
	if err := ctx.Err(); err != nil {
		return agentservice.Response{Status: agent.RunCanceled}, err
	}
	stats := agent.RunStats{
		Status:              agent.RunSucceeded,
		StepCount:           4,
		ModelCalls:          2,
		ToolCalls:           1,
		SuccessfulToolCalls: 1,
	}
	return agentservice.Response{
		Answer: "dry-run answer",
		Status: agent.RunSucceeded,
		Stats:  &stats,
	}, nil
}

func newLiveAnswerer(ctx context.Context) (agentservice.Answerer, func(), error) {
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
	modelClient := modelclient.NewHTTPClient(&http.Client{Timeout: 10 * time.Second}, cfg.ModelProviderAllowedHosts)
	embeddingService := modelruntime.NewEmbeddingService(providerStore, modelClient, cfg.ModelProviderAPIKeyEnvVar, os.LookupEnv)
	chatService := modelruntime.NewChatService(providerStore, modelClient, cfg.ModelProviderAPIKeyEnvVar, os.LookupEnv)
	searchService := retrieval.NewService(embeddingService, documentchunk.NewPostgresStore(db))
	var historySummarizer agentservice.HistorySummarizer
	if cfg.AgentHistorySummaryEnabled {
		historySummarizer = agentservice.NewModelHistorySummarizerWithTimeout(chatService, cfg.AgentHistorySummaryTimeout)
	}
	answerService, err := agentservice.NewServiceWithLimitsAndSummarizer(
		chatService,
		searchService,
		cfg.AgentMaxSteps,
		cfg.AgentTimeout,
		cfg.AgentMaxToolResultBytes,
		cfg.AgentMaxHistoryMessages,
		cfg.AgentMaxHistoryBytes,
		historySummarizer,
	)
	if err != nil {
		closeDB()
		return nil, func() {}, fmt.Errorf("create agent service: %w", err)
	}
	return answerService, closeDB, nil
}
