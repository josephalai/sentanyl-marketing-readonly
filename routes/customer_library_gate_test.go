package routes

import (
	"testing"
	"time"

	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
)

// freshEnrollment returns an enrollment that just started, optionally with a
// completed lesson.
func freshEnrollment(t time.Time, completed ...string) *pkgmodels.CourseEnrollment {
	e := &pkgmodels.CourseEnrollment{EnrolledAt: t}
	for i := 0; i < len(completed); i += 2 {
		mod, lesson := completed[i], completed[i+1]
		ts := t.Add(time.Minute)
		e.Progress = append(e.Progress, &pkgmodels.LessonProgress{
			ModuleSlug: mod, LessonSlug: lesson,
			Completed: true, CompletedAt: &ts,
			WatchPercent: 100,
		})
	}
	return e
}

func TestResolveGate_DripUnlocksAfterDelay(t *testing.T) {
	now := time.Now()
	enroll := freshEnrollment(now.Add(-2 * time.Minute))
	lesson := &pkgmodels.CourseLesson{Slug: "l1", DripMinutes: 1}
	g := resolveGate(now, &pkgmodels.Product{}, lesson, enroll, true, true)
	if g.locked {
		t.Errorf("expected unlocked after drip, got locked: reason=%q", g.reason)
	}
}

func TestResolveGate_DripStillLocked(t *testing.T) {
	now := time.Now()
	enroll := freshEnrollment(now)
	lesson := &pkgmodels.CourseLesson{Slug: "l1", DripMinutes: 5}
	g := resolveGate(now, &pkgmodels.Product{}, lesson, enroll, true, true)
	if !g.locked || g.reason != "drip" {
		t.Errorf("expected drip lock, got locked=%v reason=%q", g.locked, g.reason)
	}
}

func TestResolveGate_FreeBypassesDrip(t *testing.T) {
	now := time.Now()
	enroll := freshEnrollment(now)
	lesson := &pkgmodels.CourseLesson{Slug: "l1", DripDays: 7, IsFree: true}
	g := resolveGate(now, &pkgmodels.Product{}, lesson, enroll, false, true)
	if g.locked {
		t.Errorf("free lesson should bypass drip, got locked: %q", g.reason)
	}
}

func TestResolveGate_LiveWindowBeforeStart(t *testing.T) {
	now := time.Now()
	starts := now.Add(time.Hour)
	lesson := &pkgmodels.CourseLesson{Slug: "l1", LiveStartsAt: &starts, IsFree: true}
	g := resolveGate(now, &pkgmodels.Product{}, lesson, freshEnrollment(now), true, true)
	if !g.locked || g.reason != "live_not_started" {
		t.Errorf("expected live_not_started, got locked=%v reason=%q", g.locked, g.reason)
	}
}

func TestResolveGate_LiveWindowAfterEnd(t *testing.T) {
	now := time.Now()
	ended := now.Add(-time.Hour)
	lesson := &pkgmodels.CourseLesson{Slug: "l1", LiveEndsAt: &ended, IsFree: true}
	g := resolveGate(now, &pkgmodels.Product{}, lesson, freshEnrollment(now), true, true)
	if !g.locked || g.reason != "live_ended" {
		t.Errorf("expected live_ended, got locked=%v reason=%q", g.locked, g.reason)
	}
}

func TestResolveGate_SequentialLocksWhenPriorIncomplete(t *testing.T) {
	now := time.Now()
	enroll := freshEnrollment(now.Add(-time.Hour))
	lesson := &pkgmodels.CourseLesson{Slug: "l2"}
	p := &pkgmodels.Product{SequentialGating: true}
	g := resolveGate(now, p, lesson, enroll, false /* prior NOT complete */, true)
	if !g.locked || g.reason != "sequential" {
		t.Errorf("expected sequential lock, got locked=%v reason=%q", g.locked, g.reason)
	}
}

func TestResolveGate_QuizGate(t *testing.T) {
	now := time.Now()
	enroll := freshEnrollment(now.Add(-time.Hour))
	lesson := &pkgmodels.CourseLesson{Slug: "m2-l1"}
	p := &pkgmodels.Product{RequireQuizPass: true}
	g := resolveGate(now, p, lesson, enroll, true, false /* prior module quiz NOT passed */)
	if !g.locked || g.reason != "quiz_required" {
		t.Errorf("expected quiz_required, got locked=%v reason=%q", g.locked, g.reason)
	}
}

func TestResolveGate_CohortDripUsesFixedAnchor(t *testing.T) {
	now := time.Now()
	cohortStart := now.Add(time.Hour) // fixed anchor in the future
	p := &pkgmodels.Product{DripAnchor: "fixed_date", DripAnchorDate: &cohortStart}
	enroll := freshEnrollment(now.Add(-30 * 24 * time.Hour)) // long-since enrolled
	lesson := &pkgmodels.CourseLesson{Slug: "l1"}            // no drip delay
	g := resolveGate(now, p, lesson, enroll, true, true)
	if !g.locked || g.reason != "drip" {
		t.Errorf("expected drip lock for cohort, got locked=%v reason=%q at %v", g.locked, g.reason, g.availableAt)
	}
}

func TestApplyLessonTranslation_FallsBackToBaseLanguage(t *testing.T) {
	l := &pkgmodels.CourseLesson{
		Title:       "Hello",
		ContentHTML: "<p>en</p>",
		Translations: map[string]*pkgmodels.LessonTranslation{
			"es": {Title: "Hola", ContentHTML: "<p>es</p>"},
		},
	}
	title, html := applyLessonTranslation(l, "es-MX")
	if title != "Hola" || html != "<p>es</p>" {
		t.Errorf("expected fallback to es, got title=%q html=%q", title, html)
	}
	title, html = applyLessonTranslation(l, "fr")
	if title != "Hello" || html != "<p>en</p>" {
		t.Errorf("expected fallback to base, got title=%q html=%q", title, html)
	}
}

func TestShouldIssueCertificate(t *testing.T) {
	True, False := true, false
	cases := []struct {
		name   string
		tenant *pkgmodels.Tenant
		prod   *pkgmodels.Product
		want   bool
	}{
		{"defaults to enabled", &pkgmodels.Tenant{}, &pkgmodels.Product{}, true},
		{"tenant disabled, course unset", &pkgmodels.Tenant{CertificatesDefaultEnabled: &False}, &pkgmodels.Product{}, false},
		{"tenant disabled, course explicit on", &pkgmodels.Tenant{CertificatesDefaultEnabled: &False}, &pkgmodels.Product{CertificateEnabled: &True}, true},
		{"tenant on, course explicit off", &pkgmodels.Tenant{CertificatesDefaultEnabled: &True}, &pkgmodels.Product{CertificateEnabled: &False}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldIssueCertificate(tc.tenant, tc.prod); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}
