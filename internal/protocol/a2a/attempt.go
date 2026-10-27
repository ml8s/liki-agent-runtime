package a2a

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	"github.com/a2aproject/a2a-go/v2/a2asrv"
)

type executionCancelKey struct{}

type executionAttemptKey struct{}

type executionAttempt struct {
	ID        string
	TaskID    string
	ContextID string
	MessageID string
}

func executionAttemptFromRequest(request *a2asrv.ExecutorContext) executionAttempt {
	if request == nil {
		return executionAttempt{}
	}
	messageID := ""
	if request.Message != nil {
		messageID = request.Message.ID
	}
	return executionAttempt{
		TaskID:    string(request.TaskID),
		ContextID: request.ContextID,
		MessageID: messageID,
	}
}

func (a executionAttempt) withID() executionAttempt {
	digest := sha256.Sum256([]byte(a.TaskID + "\x00" + a.ContextID + "\x00" + a.MessageID))
	a.ID = hex.EncodeToString(digest[:])
	return a
}

func executionAttemptFromContext(ctx context.Context, request *a2asrv.ExecutorContext) executionAttempt {
	if attemptID, ok := ctx.Value(executionAttemptKey{}).(string); ok && attemptID != "" {
		attempt := executionAttemptFromRequest(request)
		attempt.ID = attemptID
		return attempt
	}
	return executionAttemptFromRequest(request).withID()
}
