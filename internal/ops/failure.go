package ops

import (
	"context"
	"errors"
	"net"
	"strings"
)

type FailureClass string

const (
	FailureCanceled       FailureClass = "canceled"
	FailureTimeout        FailureClass = "timeout"
	FailureRateLimited    FailureClass = "rate_limited"
	FailureAuthentication FailureClass = "authentication"
	FailureInvalidRequest FailureClass = "invalid_request"
	FailureUnavailable    FailureClass = "unavailable"
	FailureDependency     FailureClass = "dependency"
	FailureUnknown        FailureClass = "unknown"
)

type httpStatusError interface{ HTTPStatus() int }

func ClassifyFailure(err error) FailureClass {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return FailureCanceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return FailureTimeout
	}
	var statusErr httpStatusError
	if errors.As(err, &statusErr) {
		switch status := statusErr.HTTPStatus(); {
		case status == 401 || status == 403:
			return FailureAuthentication
		case status == 408 || status == 429:
			return FailureRateLimited
		case status >= 500:
			return FailureUnavailable
		case status >= 400:
			return FailureInvalidRequest
		}
	}
	var networkError net.Error
	if errors.As(err, &networkError) {
		if networkError.Timeout() {
			return FailureTimeout
		}
		return FailureUnavailable
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{"connection refused", "connection reset", "no such host", "temporarily unavailable", "service unavailable"} {
		if strings.Contains(message, marker) {
			return FailureUnavailable
		}
	}
	if strings.Contains(message, "database") || strings.Contains(message, "redis") {
		return FailureDependency
	}
	return FailureUnknown
}

func IsRetryableFailure(err error) bool {
	switch ClassifyFailure(err) {
	case FailureTimeout, FailureRateLimited, FailureUnavailable, FailureDependency:
		return true
	default:
		return false
	}
}
