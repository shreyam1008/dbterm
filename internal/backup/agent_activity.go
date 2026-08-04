package backup

import (
	"context"
	"encoding/json"
	"sync"
	"time"
)

const agentActivityWriteInterval = time.Second

const (
	agentActivityWriteTimeout = 500 * time.Millisecond
	agentActivityClearTimeout = 2 * time.Second
)

type agentActivityRecorder struct {
	store *Store
	base  AgentActivity

	mu        sync.Mutex
	lastPhase string
	lastWrite time.Time
}

func newAgentActivityRecorder(store *Store, job Job, run Run) *agentActivityRecorder {
	return &agentActivityRecorder{
		store: store,
		base: AgentActivity{
			JobID:     job.ID,
			JobName:   job.Name,
			RunID:     run.ID,
			StartedAt: run.StartedAt.UTC(),
		},
	}
}

func (recorder *agentActivityRecorder) Report(event ProgressEvent) {
	if recorder == nil || recorder.store == nil {
		return
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	now := time.Now().UTC()
	if event.Phase == recorder.lastPhase && !recorder.lastWrite.IsZero() && now.Sub(recorder.lastWrite) < agentActivityWriteInterval {
		return
	}
	activity := recorder.base
	activity.Phase = event.Phase
	activity.Message = event.Message
	activity.CurrentBytes = event.CurrentBytes
	activity.TotalBytes = event.TotalBytes
	activity.UpdatedAt = now
	payload, err := json.Marshal(activity)
	if err != nil {
		return
	}
	writeContext, cancel := context.WithTimeout(context.Background(), agentActivityWriteTimeout)
	writeErr := recorder.store.SetMeta(writeContext, agentActivityKey, string(payload))
	cancel()
	if writeErr == nil {
		recorder.lastPhase = event.Phase
		recorder.lastWrite = now
	}
}

func (recorder *agentActivityRecorder) Clear() {
	if recorder == nil {
		return
	}
	clearAgentActivity(recorder.store)
}

func clearAgentActivity(store *Store) {
	if store != nil {
		clearContext, cancel := context.WithTimeout(context.Background(), agentActivityClearTimeout)
		_ = store.SetMeta(clearContext, agentActivityKey, "")
		cancel()
	}
}
