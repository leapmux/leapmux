package kilo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/coder/quartz"
)

type kiloStoredPart struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Text string `json:"text"`
}

// readStoredMessage reads the daemon's current parts, including empty parts of failed attempts.
func (q *kiloQuestions) readStoredMessage(ctx context.Context, clock quartz.Clock, sessionID, messageID string) ([]kiloStoredPart, error) {
	base := q.base()
	if base == "" {
		return nil, fmt.Errorf("the Kilo daemon address is unavailable")
	}
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	timer := clock.AfterFunc(5*time.Second, func() { cancel(context.DeadlineExceeded) }, StoredMessageReadTimerTag)
	defer timer.Stop()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/session/"+url.PathEscape(sessionID)+"/message/"+url.PathEscape(messageID), nil)
	if err != nil {
		return nil, err
	}
	response, err := q.client.Do(request)
	if err != nil {
		return nil, errors.Join(err, context.Cause(ctx))
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the Kilo message route answered %s", response.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, kiloQuestionMaxEvent+1))
	if err != nil {
		return nil, errors.Join(err, context.Cause(ctx))
	}
	if len(raw) > kiloQuestionMaxEvent {
		return nil, fmt.Errorf("the Kilo stored message exceeds %d bytes", kiloQuestionMaxEvent)
	}
	var message struct {
		Info struct {
			ID        string `json:"id"`
			SessionID string `json:"sessionID"`
		} `json:"info"`
		Parts *[]kiloStoredPart `json:"parts"`
	}
	if err := json.Unmarshal(raw, &message); err != nil {
		return nil, err
	}
	if message.Info.ID != messageID || message.Info.SessionID != sessionID || message.Parts == nil {
		return nil, fmt.Errorf("the Kilo stored message has an invalid identity or parts field")
	}
	return *message.Parts, nil
}

// storedAssistantError is the error of the newest stored message of a session.
type storedAssistantError struct {
	id      string
	created time.Time
	err     DaemonError
	// info is the info object of the message, the record that holds the error.
	info json.RawMessage
}

// readLatestAssistantError reads the newest stored message of sessionID. It
// fails unless that message is an assistant message that holds an error. The
// read ends after latestAssistantErrorReadTimeout on clock.
func (q *kiloQuestions) readLatestAssistantError(ctx context.Context, clock quartz.Clock, sessionID string) (storedAssistantError, error) {
	if q.base() == "" {
		return storedAssistantError{}, fmt.Errorf("the Kilo daemon address is unavailable")
	}
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	timer := clock.AfterFunc(latestAssistantErrorReadTimeout, func() { cancel(context.DeadlineExceeded) }, LatestAssistantErrorReadTimerTag)
	defer timer.Stop()
	raw, _, err := q.readRoute(ctx, "/session/"+url.PathEscape(sessionID)+"/message?"+url.Values{"limit": {"1"}}.Encode())
	if err != nil {
		return storedAssistantError{}, errors.Join(err, context.Cause(ctx))
	}
	var messages []struct {
		Info json.RawMessage `json:"info"`
	}
	if err := json.Unmarshal(raw, &messages); err != nil {
		return storedAssistantError{}, fmt.Errorf("decode the newest Kilo stored message: %w", err)
	}
	if len(messages) == 0 {
		return storedAssistantError{}, errors.New("the Kilo session stores no message")
	}
	var info struct {
		ID   string `json:"id"`
		Role string `json:"role"`
		Time struct {
			// Created is the creation instant in epoch milliseconds.
			Created int64 `json:"created"`
		} `json:"time"`
		Error *DaemonError `json:"error"`
	}
	if err := json.Unmarshal(messages[0].Info, &info); err != nil {
		return storedAssistantError{}, fmt.Errorf("decode the info of the newest Kilo stored message: %w", err)
	}
	if info.Role != "assistant" || info.Error == nil {
		return storedAssistantError{}, fmt.Errorf("the newest Kilo stored message %q holds no assistant error", info.ID)
	}
	return storedAssistantError{id: info.ID, created: time.UnixMilli(info.Time.Created), err: *info.Error, info: messages[0].Info}, nil
}

const (
	// kiloTaskPartPageSize is how many stored messages one read of the
	// parent's history takes, newest first.
	kiloTaskPartPageSize = 10
	// kiloTaskPartMaxPages limits that read to the newest hundred messages.
	// A running task call sits in the parent's newest assistant message: that
	// message waits for the task, and only a steered prompt can follow it.
	kiloTaskPartMaxPages = 10
	// kiloNextCursorHeader states the cursor of the next older page.
	kiloNextCursorHeader = "X-Next-Cursor"
)

// kiloTaskPart is what LeapMux reads of one stored tool part.
type kiloTaskPart struct {
	Type   string `json:"type"`
	CallID string `json:"callID"`
	Tool   string `json:"tool"`
	State  struct {
		Status   string `json:"status"`
		Metadata struct {
			// SessionID is the child session that a task call runs. The task
			// tool states it before the child's first request.
			SessionID string `json:"sessionId"`
		} `json:"metadata"`
	} `json:"state"`
}

// findTaskPart reads the stored task part of callID in the session sessionID.
// It reads the stored history newest first, one page at a time, and stops at
// the page that holds the part. found is false when the newest
// kiloTaskPartMaxPages pages hold no such part.
func (q *kiloQuestions) findTaskPart(ctx context.Context, sessionID, callID string) (part kiloTaskPart, found bool, err error) {
	cursor := ""
	for range kiloTaskPartMaxPages {
		var parts []json.RawMessage
		parts, cursor, err = q.readMessagePage(ctx, sessionID, cursor)
		if err != nil {
			return kiloTaskPart{}, false, err
		}
		for _, raw := range parts {
			var candidate kiloTaskPart
			// A part of another shape tells nothing about this call, so it
			// does not fail the read.
			if json.Unmarshal(raw, &candidate) != nil {
				continue
			}
			if candidate.Type == kiloPartTypeTool && candidate.Tool == kiloTaskTool && candidate.CallID == callID {
				return candidate, true, nil
			}
		}
		if cursor == "" {
			break
		}
	}
	return kiloTaskPart{}, false, nil
}

// readMessagePage reads one page of stored messages of sessionID: the newest
// page when before is empty, and the page older than before otherwise. It
// returns every part of the page, and the cursor of the next older page, or ""
// when no older page exists.
func (q *kiloQuestions) readMessagePage(ctx context.Context, sessionID, before string) ([]json.RawMessage, string, error) {
	query := url.Values{"limit": {strconv.Itoa(kiloTaskPartPageSize)}}
	if before != "" {
		query.Set("before", before)
	}
	raw, header, err := q.readRoute(ctx, "/session/"+url.PathEscape(sessionID)+"/message?"+query.Encode())
	if err != nil {
		return nil, "", err
	}
	var messages []struct {
		Parts []json.RawMessage `json:"parts"`
	}
	if err := json.Unmarshal(raw, &messages); err != nil {
		return nil, "", fmt.Errorf("decode the Kilo session messages: %w", err)
	}
	var parts []json.RawMessage
	for _, message := range messages {
		parts = append(parts, message.Parts...)
	}
	return parts, header.Get(kiloNextCursorHeader), nil
}

// readRoute GETs one route of the daemon. It returns the body and the headers
// of the answer, which must be 200 OK and at most kiloQuestionMaxEvent bytes.
func (q *kiloQuestions) readRoute(ctx context.Context, route string) ([]byte, http.Header, error) {
	base := q.base()
	if base == "" {
		return nil, nil, fmt.Errorf("the Kilo daemon address is unavailable")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base+route, nil)
	if err != nil {
		return nil, nil, err
	}
	response, err := q.client.Do(request)
	if err != nil {
		return nil, nil, fmt.Errorf("read the Kilo route %s: %w", route, err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(response.Body, 1<<12))
		return nil, nil, fmt.Errorf("the Kilo route %s answered %s: %s", route, response.Status, bytes.TrimSpace(detail))
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, kiloQuestionMaxEvent+1))
	if err != nil {
		return nil, nil, fmt.Errorf("read the Kilo route %s: %w", route, err)
	}
	if len(raw) > kiloQuestionMaxEvent {
		return nil, nil, fmt.Errorf("the Kilo route %s answered more than %d bytes", route, kiloQuestionMaxEvent)
	}
	return raw, response.Header, nil
}

// abortSession asks the daemon to cancel the running work of sessionID.
func (q *kiloQuestions) abortSession(ctx context.Context, sessionID string) error {
	base := q.base()
	if base == "" {
		return fmt.Errorf("the Kilo daemon address is unavailable")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/session/"+url.PathEscape(sessionID)+"/abort", nil)
	if err != nil {
		return err
	}
	response, err := q.client.Do(request)
	if err != nil {
		return fmt.Errorf("send the Kilo session abort: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		detail, _ := io.ReadAll(io.LimitReader(response.Body, 1<<12))
		return fmt.Errorf("the Kilo session abort answered %s: %s", response.Status, bytes.TrimSpace(detail))
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<12))
	return nil
}

// readPendingQuestions accepts only a complete native array within the record limit.
// An invalid list cannot prove that an existing question was answered.
func (q *kiloQuestions) readPendingQuestions(ctx context.Context) ([]json.RawMessage, error) {
	raw, _, err := q.readRoute(ctx, QuestionRoot)
	if err != nil {
		return nil, err
	}
	var questions *[]json.RawMessage
	if err := json.Unmarshal(raw, &questions); err != nil {
		return nil, fmt.Errorf("decode pending Kilo questions: %w", err)
	}
	if questions == nil {
		return nil, fmt.Errorf("the Kilo question list is not an array")
	}
	return *questions, nil
}

func (q *kiloQuestions) post(ctx context.Context, questionID, action string, payload any) error {
	ctx, cancel := context.WithTimeout(ctx, kiloQuestionTimeout)
	defer cancel()
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("encode the Kilo question %s: %w", action, err)
		}
		body = bytes.NewReader(encoded)
	}
	url := q.base() + QuestionRoot + "/" + questionID + "/" + action
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
	if err != nil {
		return fmt.Errorf("build the Kilo question %s: %w", action, err)
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := q.client.Do(request)
	if err != nil {
		return fmt.Errorf("send the Kilo question %s: %w", action, err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		detail, _ := io.ReadAll(io.LimitReader(response.Body, 1<<12))
		return fmt.Errorf("the Kilo question %s answered %s: %s", action, response.Status, bytes.TrimSpace(detail))
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<12))
	return nil
}
