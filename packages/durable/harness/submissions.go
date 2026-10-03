package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/minifish-org/pith/packages/ai/types"
	"github.com/minifish-org/pith/packages/durable"
)

// Submission is an awaitable handle for one durably admitted submission.
type Submission struct {
	id durable.SubmissionID
	h  *Harness
}

// ID returns the submission ID.
func (s *Submission) ID() durable.SubmissionID { return s.id }

// Status returns the latest committed submission record.
func (s *Submission) Status(ctx context.Context) (*durable.SubmissionRecord, error) {
	return s.h.submissionStatus(ctx, s.id)
}

// Wait resolves with the settled submission record.
func (s *Submission) Wait(ctx context.Context) (*durable.SubmissionRecord, error) {
	return s.h.waitSubmission(ctx, s.id)
}

// Abort withdraws a queued submission.
func (s *Submission) Abort(ctx context.Context) (string, error) {
	result, err := s.h.abortSubmission(ctx, s.id, 0)
	if err != nil {
		return "", err
	}
	if result == "not_found" {
		return "", fmt.Errorf("Submission %d does not exist", s.id)
	}
	return result, nil
}

func isSettled(record *durable.SubmissionRecord) bool {
	return record != nil && (record.Status == durable.SubmissionStatusDone || record.Status == durable.SubmissionStatusUnanswered)
}

// submit admits a submission in one commit.
func (h *Harness) submit(ctx context.Context, conversationID durable.ConversationID, draft SubmissionDraft) (*Submission, error) {
	h.scheduler.resume()
	scope := durable.TransactionScope{ConversationID: &conversationID}
	var id durable.SubmissionID
	err := h.session.CommitTransaction(ctx, scope, func(tx *durable.Transaction) error {
		var err error
		id, err = admitSubmission(ctx, tx, conversationID, draft, h.now(), h.settings())
		return err
	})
	if err != nil {
		return nil, err
	}
	return &Submission{id: id, h: h}, nil
}

// admitSubmission admits a submission inside a commit callback.
func admitSubmission(ctx context.Context, tx durable.Tx, conversationID durable.ConversationID, draft SubmissionDraft, now int64, settings Settings) (durable.SubmissionID, error) {
	if draft.RequestID != "" {
		existing, err := tx.SubmissionByRequest(ctx, conversationID, draft.RequestID)
		if err != nil {
			return 0, err
		}
		if existing != nil {
			if existing.Type != draft.Type {
				return 0, fmt.Errorf("Request %s already identifies a submission of type %s", draft.RequestID, existing.Type)
			}
			return existing.ID, nil
		}
	}
	live, err := loadLive(ctx, tx, conversationID)
	if err != nil {
		return 0, err
	}
	busy := liveRun(live) != nil
	if busy && draft.Type == durable.SubmissionTypeInput && draft.WhenBusy == QueueReject {
		return 0, &durable.ConversationBusy{ConversationID: conversationID}
	}
	var requestID *string
	if draft.RequestID != "" {
		value := draft.RequestID
		requestID = &value
	}
	var b *boundary
	if !busy {
		b, err = prepareBoundary(ctx, tx, conversationID, settings.SteeringMode, settings.FollowUpMode)
		if err != nil {
			return 0, err
		}
	}
	if b == nil || len(inboxItems(b.inbox)) > 0 {
		created, err := tx.CreateSubmission(ctx, durable.SubmissionCreate{
			ConversationID: conversationID,
			RequestID:      requestID,
			Type:           draft.Type,
			Status:         durable.SubmissionStatusQueued,
		})
		if err != nil {
			return 0, err
		}
		var inbox map[string]any
		if b != nil {
			inbox = b.inbox
		} else {
			inbox, err = loadLiveInbox(ctx, tx, conversationID)
			if err != nil {
				return 0, err
			}
		}
		items := inboxItems(inbox)
		if draft.Type == durable.SubmissionTypeWrite {
			items = append(items, map[string]any{"id": int64(created.ID), "mode": "write", "entry": entryToMap(*draft.Entry)})
		} else {
			mode := QueueFollowUp
			if draft.WhenBusy == QueueSteer {
				mode = QueueSteer
			}
			content, err := json.Marshal(draft.Content)
			if err != nil {
				return 0, err
			}
			var contentValue any
			if err := json.Unmarshal(content, &contentValue); err != nil {
				return 0, err
			}
			items = append(items, map[string]any{"id": int64(created.ID), "mode": mode, "content": contentValue})
		}
		inbox["items"] = items
		if b == nil {
			return created.ID, nil
		}
		result, err := applyBoundary(ctx, tx, b, "final", now)
		if err != nil {
			return 0, err
		}
		if len(result.users) > 0 {
			if err := startRun(ctx, tx, conversationID, live, result.users); err != nil {
				return 0, err
			}
		}
		return created.ID, nil
	}
	if draft.Type == durable.SubmissionTypeWrite {
		if draft.Entry == nil {
			return 0, errors.New("write submission requires an entry")
		}
		if isStale(b, *draft.Entry) {
			created, err := tx.CreateSubmission(ctx, durable.SubmissionCreate{
				ConversationID: conversationID,
				RequestID:      requestID,
				Type:           durable.SubmissionTypeWrite,
				Status:         durable.SubmissionStatusUnanswered,
				Reason:         "stale",
			})
			if err != nil {
				return 0, err
			}
			return created.ID, nil
		}
		entry, err := tx.AppendEntry(ctx, conversationID, *draft.Entry)
		if err != nil {
			return 0, err
		}
		entryID := entry.ID
		created, err := tx.CreateSubmission(ctx, durable.SubmissionCreate{
			ConversationID: conversationID,
			RequestID:      requestID,
			Type:           durable.SubmissionTypeWrite,
			Status:         durable.SubmissionStatusDone,
			Entry:          &entryID,
		})
		if err != nil {
			return 0, err
		}
		return created.ID, nil
	}
	message := userMessageFromContent(contentJSON(draft.Content), now)
	entry, err := tx.AppendEntry(ctx, conversationID, durable.EntryDraft{Kind: entryKindUser, Model: []types.Message{message}})
	if err != nil {
		return 0, err
	}
	entryID := entry.ID
	created, err := tx.CreateSubmission(ctx, durable.SubmissionCreate{
		ConversationID: conversationID,
		RequestID:      requestID,
		Type:           durable.SubmissionTypeInput,
		Status:         durable.SubmissionStatusPlaced,
		Entry:          &entryID,
	})
	if err != nil {
		return 0, err
	}
	if err := startRun(ctx, tx, conversationID, live, []durable.SubmissionID{created.ID}); err != nil {
		return 0, err
	}
	return created.ID, nil
}

// contentJSON returns the JSON representation of a submission content.
func contentJSON(content types.UserContent) any {
	data, err := json.Marshal(content)
	if err != nil {
		return nil
	}
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return nil
	}
	return value
}
