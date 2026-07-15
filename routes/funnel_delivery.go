package routes

import (
	"context"
	"fmt"

	"github.com/josephalai/sentanyl/pkg/db"
	"github.com/josephalai/sentanyl/pkg/deliveryevents"
	"github.com/josephalai/sentanyl/pkg/jobs"
	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
	"gopkg.in/mgo.v2/bson"
)

// RegisterFunnelDeliveryEventJob installs the first-class DEL-017 projection:
// canonical media event -> published stage trigger -> durable Story command.
func RegisterFunnelDeliveryEventJob() {
	jobs.Register(deliveryevents.JobType, handleFunnelDeliveryEvent)
}

func handleFunnelDeliveryEvent(_ context.Context, job *jobs.Job) error {
	tenantHex := payloadString(job, "tenant_id")
	contactPublicID := payloadString(job, "contact_public_id")
	funnelPublicID := payloadString(job, "funnel_public_id")
	stagePublicID := payloadString(job, "stage_public_id")
	if !bson.IsObjectIdHex(tenantHex) || contactPublicID == "" || funnelPublicID == "" || stagePublicID == "" {
		return fmt.Errorf("delivery.funnel.event: incomplete identity")
	}
	tenantID := bson.ObjectIdHex(tenantHex)
	var contact pkgmodels.User
	if err := db.GetCollection(pkgmodels.UserCollection).Find(bson.M{
		"tenant_id": tenantID, "public_id": contactPublicID, "timestamps.deleted_at": nil,
	}).One(&contact); err != nil {
		return fmt.Errorf("delivery.funnel.event: verified contact: %w", err)
	}
	var funnel pkgmodels.Funnel
	if err := db.GetCollection(pkgmodels.FunnelCollection).Find(bson.M{
		"tenant_id": tenantID, "public_id": funnelPublicID, "status": "published", "timestamps.deleted_at": nil,
	}).One(&funnel); err != nil {
		return fmt.Errorf("delivery.funnel.event: published funnel: %w", err)
	}
	var stage *pkgmodels.FunnelStage
	for _, route := range funnel.PublishedRoutes {
		if route == nil {
			continue
		}
		for _, candidate := range route.Stages {
			if candidate != nil && candidate.PublicId == stagePublicID {
				stage = candidate
				break
			}
		}
	}
	if stage == nil {
		return fmt.Errorf("delivery.funnel.event: stage %s is not in published snapshot", stagePublicID)
	}
	eventName := payloadString(job, "event_name")
	blockID := payloadString(job, "block_id")
	mediaID := payloadString(job, "media_public_id")
	progress := payloadInt(job, "progress_pct")
	// The browser may report Funnel/stage/block context, but it cannot choose
	// automation authority. Prove the block is part of the immutable published
	// stage and is bound to the media identity from the signed player token.
	if !publishedStageContainsMedia(stage, blockID, mediaID) {
		return nil
	}
	for _, trigger := range stage.Triggers {
		if deliveryTriggerMatches(trigger, eventName, blockID, mediaID, progress) && trigger.DoAction != nil {
			key := "delivery:" + payloadString(job, "event_id") + ":" + trigger.PublicId
			executeFunnelActionWithKey(tenantHex, trigger.DoAction, contact.PublicId, funnel.PublicId, key)
		}
	}
	return nil
}

func publishedStageContainsMedia(stage *pkgmodels.FunnelStage, blockID, mediaID string) bool {
	if stage == nil || blockID == "" || mediaID == "" {
		return false
	}
	for _, page := range stage.Pages {
		if page == nil {
			continue
		}
		for _, block := range page.Blocks {
			if block != nil && block.BlockType == "video" && block.MediaPublicId == mediaID &&
				(block.SectionID == blockID || block.PublicId == blockID) {
				return true
			}
		}
	}
	return false
}

func deliveryTriggerMatches(trigger *pkgmodels.Trigger, eventName, blockID, mediaID string, progress int) bool {
	if trigger == nil {
		return false
	}
	wanted := map[string]string{
		pkgmodels.VideoEventPlay: pkgmodels.OnPlay, pkgmodels.VideoEventPause: pkgmodels.OnPause,
		pkgmodels.VideoEventComplete: pkgmodels.OnComplete, pkgmodels.VideoEventRewatch: pkgmodels.OnRewatch,
		pkgmodels.VideoEventCTAClick: pkgmodels.OnCTAClick, pkgmodels.VideoEventChapterClick: pkgmodels.OnChapterClick,
		pkgmodels.VideoEventTurnstileSubmit: pkgmodels.OnTurnstileSubmit,
	}[eventName]
	if eventName == pkgmodels.VideoEventProgress {
		if trigger.TriggerType != pkgmodels.OnProgress && trigger.TriggerType != pkgmodels.OnWatch {
			return false
		}
	} else if wanted == "" || trigger.TriggerType != wanted {
		return false
	}
	target := trigger.UserActionValue
	if trigger.TriggerType == pkgmodels.OnWatch && trigger.WatchBlockID != "" {
		target = trigger.WatchBlockID
		if !evaluateWatchThreshold(trigger.WatchOperator, progress, trigger.WatchPercent) {
			return false
		}
	}
	return target == "" || target == blockID || target == mediaID
}

func payloadString(job *jobs.Job, key string) string {
	v, _ := job.Payload[key].(string)
	return v
}

func payloadInt(job *jobs.Job, key string) int {
	switch v := job.Payload[key].(type) {
	case int:
		return v
	case int32:
		return int(v)
	case int64:
		return int(v)
	case float64:
		return int(v)
	default:
		return 0
	}
}
