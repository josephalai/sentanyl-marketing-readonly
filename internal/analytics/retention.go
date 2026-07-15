package analytics

import (
	"context"
	"log"
	"os"
	"strconv"
	"time"

	"gopkg.in/mgo.v2/bson"

	"github.com/josephalai/sentanyl/pkg/db"
	"github.com/josephalai/sentanyl/pkg/jobs"
	models "github.com/josephalai/sentanyl/pkg/models"
)

// ANA-011: explicit, env-configurable retention for event stores. Revenue
// facts and purchase logs are FINANCIAL RECORDS and are deliberately exempt
// — they never age out here. Windows are per-collection env vars (days);
// 0 disables that collection's sweep. Policy doc:
// docs/specs/analytics-retention.md.
const retentionJobType = "analytics.retention.sweep"

type retentionTarget struct {
	collection string
	envVar     string
	defDays    int
	timeField  string
}

var retentionTargets = []retentionTarget{
	{models.FormSubmissionCollection, "RETENTION_FORM_SUBMISSIONS_DAYS", 730, "created_at"},
	{models.EmailSendCollection, "RETENTION_EMAIL_SENDS_DAYS", 730, "sent_at"},
	{models.ProviderEventCollection, "RETENTION_PROVIDER_EVENTS_DAYS", 365, "created_at"},
}

func retentionDays(t retentionTarget) int {
	if v := os.Getenv(t.envVar); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return n
		}
	}
	return t.defDays
}

// StartRetentionSweep registers + arms the daily analytics retention sweep.
// Call once from marketing main.
func StartRetentionSweep() {
	jobs.Register(retentionJobType, func(_ context.Context, job *jobs.Job) error {
		_ = jobs.EnqueueSweep(retentionJobType, time.Now().Add(24*time.Hour), 24*time.Hour)
		for _, t := range retentionTargets {
			days := retentionDays(t)
			if days <= 0 {
				continue
			}
			cutoff := time.Now().AddDate(0, 0, -days)
			info, err := db.GetCollection(t.collection).RemoveAll(bson.M{
				t.timeField: bson.M{"$lt": cutoff},
			})
			if err != nil {
				log.Printf("analytics retention: %s: %v", t.collection, err)
				continue
			}
			if info.Removed > 0 {
				log.Printf("analytics retention: removed %d rows from %s older than %dd", info.Removed, t.collection, days)
			}
		}
		return nil
	})
	_ = jobs.EnqueueSweep(retentionJobType, time.Now().Add(2*time.Minute), 24*time.Hour)
}
