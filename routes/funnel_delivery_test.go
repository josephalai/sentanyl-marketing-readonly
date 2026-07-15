package routes

import (
	"testing"

	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
)

func TestDeliveryTriggerMatches(t *testing.T) {
	watch := &pkgmodels.Trigger{TriggerType: pkgmodels.OnWatch, WatchBlockID: "hero", WatchOperator: ">=", WatchPercent: 75}
	if deliveryTriggerMatches(watch, pkgmodels.VideoEventProgress, "hero", "media-1", 74) {
		t.Fatal("watch fired below threshold")
	}
	if !deliveryTriggerMatches(watch, pkgmodels.VideoEventProgress, "hero", "media-1", 75) {
		t.Fatal("watch did not fire at threshold")
	}
	if deliveryTriggerMatches(watch, pkgmodels.VideoEventProgress, "other", "media-1", 90) {
		t.Fatal("watch fired for another block")
	}
	complete := &pkgmodels.Trigger{TriggerType: pkgmodels.OnComplete, UserActionValue: "media-1"}
	if !deliveryTriggerMatches(complete, pkgmodels.VideoEventComplete, "", "media-1", 100) {
		t.Fatal("completion did not match media target")
	}
	if deliveryTriggerMatches(complete, pkgmodels.VideoEventPause, "", "media-1", 50) {
		t.Fatal("completion trigger fired on pause")
	}
}

func TestPublishedStageContainsMedia(t *testing.T) {
	stage := pkgmodels.NewFunnelStage()
	page := pkgmodels.NewFunnelPage()
	block := &pkgmodels.PageBlock{SectionID: "hero", BlockType: "video", MediaPublicId: "media-1"}
	page.Blocks = []*pkgmodels.PageBlock{block}
	stage.Pages = []*pkgmodels.FunnelPage{page}
	if !publishedStageContainsMedia(stage, "hero", "media-1") {
		t.Fatal("published managed-media block was not recognized")
	}
	if publishedStageContainsMedia(stage, "hero", "media-2") || publishedStageContainsMedia(stage, "forged", "media-1") {
		t.Fatal("unbound client context was accepted")
	}
}
