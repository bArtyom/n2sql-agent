package handler_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/followup"
	"github.com/bArtyom/n2sql-agent/internal/handler"
)

type suggesterStub struct {
	questions []followup.Suggestion
	err       error
	kbID      int64
}

func (s *suggesterStub) Suggest(_ context.Context, knowledgeBaseID int64, _, _ string) ([]followup.Suggestion, error) {
	s.kbID = knowledgeBaseID
	return s.questions, s.err
}

func TestFollowUpSuggestionsReturnsQuestions(t *testing.T) {
	suggester := &suggesterStub{questions: []followup.Suggestion{{ID: "follow-up-1", Text: "请举例"}}}
	request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/7/follow-up-suggestions", strings.NewReader(`{"question":"问题","answer":"回答"}`))
	request.SetPathValue("id", "7")
	response := httptest.NewRecorder()

	handler.NewFollowUpSuggestions(suggester).ServeHTTP(response, request)

	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "请举例") || suggester.kbID != 7 {
		t.Fatalf("response = %d %s, kb = %d", response.Code, response.Body.String(), suggester.kbID)
	}
}

func TestFollowUpSuggestionsRejectsInvalidAndHidesFailure(t *testing.T) {
	suggester := &suggesterStub{err: errors.New("provider secret detail")}
	invalid := httptest.NewRecorder()
	invalidRequest := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/7/follow-up-suggestions", strings.NewReader(`{"question":""}`))
	invalidRequest.SetPathValue("id", "7")
	handler.NewFollowUpSuggestions(suggester).ServeHTTP(invalid, invalidRequest)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid status = %d", invalid.Code)
	}

	failed := httptest.NewRecorder()
	failedRequest := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/7/follow-up-suggestions", strings.NewReader(`{"question":"问题","answer":"回答"}`))
	failedRequest.SetPathValue("id", "7")
	handler.NewFollowUpSuggestions(suggester).ServeHTTP(failed, failedRequest)
	if failed.Code != http.StatusBadGateway || strings.Contains(failed.Body.String(), "secret detail") {
		t.Fatalf("failure response = %d %s", failed.Code, failed.Body.String())
	}
}
