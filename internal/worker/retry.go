package worker

import (
	"errors"
	"fmt"
	"time"
)

var ErrPermanent = errors.New("permanent document processing error")

func Permanent(err error) error {
	if err == nil {
		return ErrPermanent
	}
	return fmt.Errorf("%w: %w", ErrPermanent, err)
}

type RetryPolicy struct {
	MaxAttempts  int
	InitialDelay time.Duration
	MaxDelay     time.Duration
}

var DefaultRetryPolicy = RetryPolicy{
	MaxAttempts:  3,
	InitialDelay: time.Second,
	MaxDelay:     time.Minute,
}

func (p RetryPolicy) NextRetryAt(now time.Time, attempt int) (time.Time, bool) {
	if p.MaxAttempts <= 0 || attempt >= p.MaxAttempts {
		return time.Time{}, false
	}
	if attempt < 1 {
		attempt = 1
	}
	delay := p.InitialDelay
	if delay <= 0 {
		delay = time.Second
	}
	maxDelay := p.MaxDelay
	if maxDelay <= 0 || maxDelay < delay {
		maxDelay = delay
	}
	for step := 1; step < attempt && delay < maxDelay; step++ {
		if delay > maxDelay/2 {
			delay = maxDelay
			break
		}
		delay *= 2
	}
	return now.Add(delay), true
}
