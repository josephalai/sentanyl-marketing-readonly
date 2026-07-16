package site

import (
	"gopkg.in/mgo.v2/bson"
)

// ResolveSiteID accepts either an internal ObjectId hex (24 lowercase-hex chars)
// or the platform-wide public_id and returns the site's ObjectId, scoped to
// tenantID. public_ids are 22-char mixed-case nanoids, so they never satisfy
// bson.IsObjectIdHex — the discrimination is unambiguous. This lets every
// site_* API client address a site the same way every other Sentanyl surface
// does (by public_id) while remaining backward-compatible with ObjectId callers.
func ResolveSiteID(idOrPublic string, tenantID bson.ObjectId) (bson.ObjectId, error) {
	if bson.IsObjectIdHex(idOrPublic) {
		return bson.ObjectIdHex(idOrPublic), nil
	}
	s, err := GetSiteByPublicID(idOrPublic, tenantID)
	if err != nil {
		return "", err
	}
	return s.Id, nil
}

// ResolvePageID accepts either an internal ObjectId hex or a public_id and
// returns the page's ObjectId, scoped to tenantID. See ResolveSiteID.
func ResolvePageID(idOrPublic string, tenantID bson.ObjectId) (bson.ObjectId, error) {
	if bson.IsObjectIdHex(idOrPublic) {
		return bson.ObjectIdHex(idOrPublic), nil
	}
	p, err := GetSitePageByPublicID(idOrPublic, tenantID)
	if err != nil {
		return "", err
	}
	return p.Id, nil
}
