package eq

import (
	"context"
	"time"
)

// The EQ lease serializes updates; LastJob makes coordinator retries idempotent.
type maintenanceState struct {
	Count   int    `json:"count"`
	LastJob string `json:"lastJob"`
	Episode string `json:"episode"`
}

func (s *Store) resetMaintenance(ctx context.Context, job Job, recovered bool) error {
	if job.Purpose == "maintain-session" || job.DryRun || (!recovered && job.Source != "scheduled") {
		return nil
	}
	var state maintenanceState
	found, err := s.get(ctx, "maintenance-state", &state)
	if err != nil || !found {
		return err
	}
	state.Count = 0
	state.LastJob = job.JobID
	if recovered {
		state.Episode = ""
	}
	return s.put(ctx, "maintenance-state", state, job.JobID)
}

func (s *Store) maintenance(ctx context.Context, job Job) (any, error) {
	if job.Purpose != "maintain-session" && job.Source == "scheduled" && !job.DryRun {
		var state maintenanceState
		_, err := s.get(ctx, "maintenance-state", &state)
		if err != nil {
			return nil, err
		}
		if state.LastJob != job.JobID {
			state.Count++
			state.LastJob = job.JobID
			if state.Episode == "" {
				state.Episode = job.JobID
			}
			if err = s.put(ctx, "maintenance-state", state, job.JobID); err != nil {
				return nil, err
			}
		}
		if state.Count >= 2 {
			if err = s.notifyOnce(ctx, "maintenance-notification#"+state.Episode,
				"EQ Bank maintenance has prevented two consecutive scheduled imports. The next four-hour run will try again.", "EQ Bank Maintenance"); err != nil {
				return nil, err
			}
		}
	}
	outcome := "deferred-maintenance"
	if job.Source == "alert" {
		outcome = "retry-maintenance"
	}
	if job.Source == "manual" {
		outcome = "bank-maintenance"
	}
	summary := map[string]any{"version": 1, "jobId": job.JobID, "dryRun": job.DryRun,
		"maintenance": true, "outcome": outcome, "reason": "bank-maintenance",
		"missing": job.Source == "alert", "at": time.Now().UTC()}
	if err := s.upload(ctx, "jobs/"+job.JobID+"/preview.json", summary); err != nil {
		return nil, err
	}
	return summary, nil
}
