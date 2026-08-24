package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/bArtyom/n2sql-agent/internal/evaluationdataset"
	"github.com/bArtyom/n2sql-agent/internal/evaluationrun"
	"github.com/bArtyom/n2sql-agent/internal/retrievaleval"
)

type evaluationCreateRequest struct {
	evaluationdataset.Snapshot
	Config                map[string]any `json:"config,omitempty"`
	KnowledgeBaseSnapshot map[string]any `json:"knowledge_base_snapshot,omitempty"`
	ModelConfig           map[string]any `json:"model_config,omitempty"`
}

func NewEvaluation(store evaluationrun.Store, reader evaluationrun.Reader) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		knowledgeBaseID, ok := decodeKnowledgeBaseID(w, r)
		if !ok {
			return
		}
		runIDText := r.PathValue("runID")
		if r.Method == http.MethodPost && runIDText == "" {
			createEvaluation(w, r, store, knowledgeBaseID)
			return
		}
		if r.Method == http.MethodGet && runIDText != "" {
			getEvaluation(w, r, reader, knowledgeBaseID, runIDText)
			return
		}
		w.WriteHeader(http.StatusMethodNotAllowed)
	})
}

func createEvaluation(w http.ResponseWriter, r *http.Request, store evaluationrun.Store, knowledgeBaseID int64) {
	if store == nil {
		http.Error(w, `{"error":"evaluation is unavailable"}`, http.StatusNotImplemented)
		return
	}
	var request evaluationCreateRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		http.Error(w, `{"error":"invalid evaluation dataset"}`, http.StatusBadRequest)
		return
	}
	dataset := request.Dataset()
	if err := dataset.Validate(); err != nil {
		http.Error(w, `{"error":"invalid evaluation dataset"}`, http.StatusBadRequest)
		return
	}
	pairs, err := dataset.Pairs()
	if err != nil {
		http.Error(w, `{"error":"invalid evaluation dataset relations"}`, http.StatusBadRequest)
		return
	}
	if len(request.PassageChunkIDs) == 0 {
		http.Error(w, `{"error":"passage_chunk_ids are required"}`, http.StatusBadRequest)
		return
	}
	snapshot, err := json.Marshal(request.Snapshot)
	if err != nil {
		http.Error(w, `{"error":"unable to encode evaluation dataset"}`, http.StatusInternalServerError)
		return
	}
	config, err := json.Marshal(request.Config)
	if err != nil {
		http.Error(w, `{"error":"invalid evaluation config"}`, http.StatusBadRequest)
		return
	}
	knowledgeBaseSnapshot, err := json.Marshal(request.KnowledgeBaseSnapshot)
	if err != nil {
		http.Error(w, `{"error":"invalid knowledge base snapshot"}`, http.StatusBadRequest)
		return
	}
	modelConfig, err := json.Marshal(request.ModelConfig)
	if err != nil {
		http.Error(w, `{"error":"invalid model config"}`, http.StatusBadRequest)
		return
	}
	run, err := store.Create(r.Context(), evaluationrun.CreateInput{KnowledgeBaseID: knowledgeBaseID, DatasetSnapshot: snapshot, Config: config, KnowledgeBaseSnapshot: knowledgeBaseSnapshot, ModelConfig: modelConfig, TotalCases: len(pairs)})
	if err != nil {
		http.Error(w, `{"error":"unable to create evaluation"}`, http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusAccepted)
	writeJSON(w, map[string]any{"run_id": run.ID, "status": run.Status, "total_cases": run.TotalCases, "dataset_version": run.DatasetVersion})
}

func getEvaluation(w http.ResponseWriter, r *http.Request, reader evaluationrun.Reader, knowledgeBaseID int64, runIDText string) {
	if reader == nil {
		http.Error(w, `{"error":"evaluation is unavailable"}`, http.StatusNotImplemented)
		return
	}
	runID, err := strconv.ParseInt(runIDText, 10, 64)
	if err != nil || runID <= 0 {
		http.Error(w, `{"error":"invalid evaluation run ID"}`, http.StatusBadRequest)
		return
	}
	run, err := reader.Get(r.Context(), runID, knowledgeBaseID)
	if errors.Is(err, evaluationrun.ErrRunNotFound) {
		http.Error(w, `{"error":"evaluation run not found"}`, http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, `{"error":"unable to load evaluation"}`, http.StatusInternalServerError)
		return
	}
	results, err := reader.ListResults(r.Context(), run.ID)
	if err != nil {
		http.Error(w, `{"error":"unable to load evaluation results"}`, http.StatusInternalServerError)
		return
	}
	refusalMetrics := evaluationRefusalMetrics(run)
	writeJSON(w, map[string]any{"run_id": run.ID, "status": run.Status, "total_cases": run.TotalCases, "finished_cases": run.FinishedCases, "failed_cases": run.FailedCases, "attempt_count": run.AttemptCount, "dataset_version": run.DatasetVersion, "duration_ms": run.DurationMS, "prompt_tokens": run.PromptTokens, "completion_tokens": run.CompletionTokens, "total_tokens": run.TotalTokens, "estimated_cost_micros": run.EstimatedCostMicros, "expected_relevant_cases": run.ExpectedRelevantCases, "expected_irrelevant_cases": run.ExpectedIrrelevantCases, "correct_refusals": run.CorrectRefusals, "false_refusals": run.FalseRefusals, "unsupported_accepts": run.UnsupportedAccepts, "recall": refusalMetrics.recall, "refusal_rate": refusalMetrics.refusalRate, "accuracy": refusalMetrics.accuracy, "error": run.ErrorMessage, "created_at": run.CreatedAt, "started_at": run.StartedAt, "finished_at": run.FinishedAt, "updated_at": run.UpdatedAt, "metrics": summarizeEvaluationResults(results), "results": results})
}

func summarizeEvaluationResults(results []evaluationrun.CaseResult) retrievaleval.MetricSummary {
	samples := make([]retrievaleval.MetricSample, 0, len(results))
	for _, result := range results {
		if result.Status == evaluationrun.CaseStatusFailed {
			continue
		}
		sample := retrievaleval.MetricSample{IncludeQuality: !result.Refused}
		if result.ExpectedRelevant {
			if json.Unmarshal(result.RetrievalMetrics, &sample.RetrievalMetrics) == nil {
				sample.IncludeRetrieval = true
			}
		}
		if json.Unmarshal(result.GenerationMetrics, &sample.GenerationMetrics) != nil {
			continue
		}
		sample.IncludeGeneration = strings.TrimSpace(result.ReferenceAnswer) != ""
		samples = append(samples, sample)
	}
	return retrievaleval.AggregateMetricSamples(samples)
}

type evaluationRefusalMetricValues struct {
	recall      float64
	refusalRate float64
	accuracy    float64
}

func evaluationRefusalMetrics(run evaluationrun.Run) evaluationRefusalMetricValues {
	metrics := evaluationRefusalMetricValues{}
	if run.ExpectedRelevantCases > 0 {
		metrics.recall = float64(run.ExpectedRelevantCases-run.FalseRefusals) / float64(run.ExpectedRelevantCases)
	}
	if run.ExpectedIrrelevantCases > 0 {
		metrics.refusalRate = float64(run.CorrectRefusals) / float64(run.ExpectedIrrelevantCases)
	}
	if run.TotalCases > 0 {
		metrics.accuracy = float64(run.ExpectedRelevantCases-run.FalseRefusals+run.CorrectRefusals) / float64(run.TotalCases)
	}
	return metrics
}
