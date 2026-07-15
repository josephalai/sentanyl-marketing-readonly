package routes

import (
	"testing"

	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
)

func TestSnapshotFunnelRoutesIsDeepCopy(t *testing.T) {
	page := pkgmodels.NewFunnelPage()
	page.Name = "live page"
	stage := pkgmodels.NewFunnelStage()
	stage.Path = "/live"
	stage.Pages = []*pkgmodels.FunnelPage{page}
	route := pkgmodels.NewFunnelRoute()
	route.Stages = []*pkgmodels.FunnelStage{stage}

	published := snapshotFunnelRoutes([]*pkgmodels.FunnelRoute{route})
	stage.Path = "/draft"
	page.Name = "draft page"
	if got := published[0].Stages[0].Path; got != "/live" {
		t.Fatalf("published route followed draft mutation: %q", got)
	}
	if got := published[0].Stages[0].Pages[0].Name; got != "live page" {
		t.Fatalf("published page followed draft mutation: %q", got)
	}
}

func TestPrepareCompiledFunnelPublishesSnapshot(t *testing.T) {
	f := pkgmodels.NewFunnel()
	f.Name, f.Domain = "compiled", "example.test"
	route := pkgmodels.NewFunnelRoute()
	stage := pkgmodels.NewFunnelStage()
	stage.Path = "/watch"
	route.Stages = []*pkgmodels.FunnelStage{stage}
	f.Routes = []*pkgmodels.FunnelRoute{route}
	prepareCompiledFunnel(f)
	if f.Status != "published" || f.PublishedVersion != f.DraftVersion || f.PublishedAt == nil {
		t.Fatalf("compiled funnel publication metadata incomplete: %#v", f)
	}
	if f.PublishedDomain != f.Domain || f.PublishedRoutes[0].Stages[0].Path != "/watch" {
		t.Fatal("compiled funnel snapshot does not match deployed draft")
	}
}
