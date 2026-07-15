package channel

import (
	"testing"

	pkgmodels "github.com/josephalai/sentanyl/pkg/models"
	"gopkg.in/mgo.v2/bson"
)

func TestPublicVisibilityFiltersExcludeDraftAndArchived(t *testing.T) {
	tests := []struct {
		name   string
		filter bson.M
		want   []string
	}{
		{name: "products", filter: publicVisibleStatus, want: []string{pkgmodels.ProductStatusDraft, pkgmodels.ProductStatusArchived}},
		{name: "offers", filter: publicVisibleOfferStatus, want: []string{pkgmodels.OfferStatusDraft, pkgmodels.OfferStatusArchived}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, ok := tt.filter["$nin"]
			if !ok {
				t.Fatal("visibility filter must use $nin so legacy blank rows remain compatible")
			}
			got, ok := raw.([]string)
			if !ok {
				t.Fatalf("$nin type = %T", raw)
			}
			for _, status := range tt.want {
				found := false
				for _, candidate := range got {
					found = found || candidate == status
				}
				if !found {
					t.Errorf("filter does not exclude %q", status)
				}
			}
		})
	}
}
