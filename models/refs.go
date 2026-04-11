package models

import (
	"github.com/josephalai/sentanyl/pkg/db"
	"github.com/josephalai/sentanyl/pkg/models"
	"gopkg.in/mgo.v2/bson"
)

// BsonRefIds mirrors the monolith's BsonCollectionIds for reference tracking.
type BsonRefIds struct {
	CollectionName string          `bson:"collection_name" json:"collection_name"`
	Ids            []bson.ObjectId `bson:"ids" json:"ids"`
}

func NewBsonRefIds() *BsonRefIds {
	return &BsonRefIds{Ids: []bson.ObjectId{}}
}

// BsonRefId mirrors the monolith's BsonCollectionId for a single reference.
type BsonRefId struct {
	CollectionName string        `bson:"collection_name" json:"collection_name"`
	Id             bson.ObjectId `bson:"_id" json:"id"`
}

func NewBsonRefIdVals(collection string, id bson.ObjectId) *BsonRefId {
	return &BsonRefId{CollectionName: collection, Id: id}
}

// RequiredBadge references a badge that must/must-not be present.
type RequiredBadge struct {
	BadgeId *BsonRefId `bson:"badge_id,omitempty" json:"badge_id,omitempty"`
	Badge   *Badge     `bson:"badge,omitempty" json:"badge,omitempty"`
}

func (rb *RequiredBadge) Hydrate() {
	if rb.BadgeId != nil && rb.BadgeId.Id.Valid() {
		rb.Badge = &Badge{}
		db.GetCollection(rb.BadgeId.CollectionName).FindId(rb.BadgeId.Id).One(rb.Badge)
	}
}

func (rb *RequiredBadge) ReadyMongoStore() []interface{} {
	if rb.Badge != nil {
		rb.BadgeId = NewBsonRefIdVals(models.BadgeCollection, rb.Badge.Id)
	}
	copy := *rb
	copy.Badge = nil
	return []interface{}{copy}
}

// Badge mirrors the shared badge entity.
type Badge struct {
	Id       bson.ObjectId `bson:"_id" json:"id,omitempty"`
	PublicId string        `bson:"public_id" json:"public_id,omitempty"`
	Name     string        `bson:"name" json:"name,omitempty"`
	models.SoftDeletes `bson:"timestamps,omitempty" json:"timestamps,omitempty"`
}

func (b *Badge) GetIdHex() string {
	return b.Id.Hex()
}

func (b *Badge) GetId() bson.ObjectId {
	return b.Id
}

// BadgeTransaction groups badge grants and revocations.
type BadgeTransaction struct {
	Id             bson.ObjectId `bson:"_id" json:"id,omitempty"`
	GiveBadgeIds   *BsonRefIds   `bson:"give_badge_ids,omitempty" json:"-"`
	GiveBadges     []*Badge      `bson:"give_badges,omitempty" json:"give_badges,omitempty"`
	RemoveBadgeIds *BsonRefIds   `bson:"remove_badge_ids,omitempty" json:"-"`
	RemoveBadges   []*Badge      `bson:"remove_badges,omitempty" json:"remove_badges,omitempty"`
}

func NewBadgeTransaction() *BadgeTransaction {
	return &BadgeTransaction{Id: bson.NewObjectId()}
}

func (bt *BadgeTransaction) Hydrate() {
	if bt.GiveBadgeIds != nil {
		bt.GiveBadges = nil
		for _, id := range bt.GiveBadgeIds.Ids {
			b := Badge{}
			db.GetCollection(bt.GiveBadgeIds.CollectionName).FindId(id).One(&b)
			bt.GiveBadges = append(bt.GiveBadges, &b)
		}
	}
	if bt.RemoveBadgeIds != nil {
		bt.RemoveBadges = nil
		for _, id := range bt.RemoveBadgeIds.Ids {
			b := Badge{}
			db.GetCollection(bt.RemoveBadgeIds.CollectionName).FindId(id).One(&b)
			bt.RemoveBadges = append(bt.RemoveBadges, &b)
		}
	}
}

func (bt *BadgeTransaction) ReadyMongoStore() []interface{} {
	var individuals []interface{}
	if bt.GiveBadges != nil {
		bt.GiveBadgeIds = NewBsonRefIds()
		bt.GiveBadgeIds.CollectionName = models.BadgeCollection
		for _, b := range bt.GiveBadges {
			bt.GiveBadgeIds.Ids = append(bt.GiveBadgeIds.Ids, b.Id)
		}
	}
	if bt.RemoveBadges != nil {
		bt.RemoveBadgeIds = NewBsonRefIds()
		bt.RemoveBadgeIds.CollectionName = models.BadgeCollection
		for _, b := range bt.RemoveBadges {
			bt.RemoveBadgeIds.Ids = append(bt.RemoveBadgeIds.Ids, b.Id)
		}
	}
	copy := *bt
	copy.GiveBadges = nil
	copy.RemoveBadges = nil
	individuals = append(individuals, copy)
	return individuals
}

// Trigger represents an event handler on a funnel stage or story enactment.
type Trigger struct {
	Id              bson.ObjectId `bson:"_id" json:"id,omitempty"`
	PublicId        string        `bson:"public_id" json:"public_id,omitempty"`
	TriggerType     string        `bson:"trigger_type" json:"trigger_type,omitempty"`
	UserActionValue string        `bson:"user_action_value,omitempty" json:"user_action_value,omitempty"`
	WatchBlockID    string        `bson:"watch_block_id,omitempty" json:"watch_block_id,omitempty"`
	WatchOperator   string        `bson:"watch_operator,omitempty" json:"watch_operator,omitempty"`
	WatchPercent    int           `bson:"watch_percent,omitempty" json:"watch_percent,omitempty"`
	DoAction        *Action       `bson:"do_action,omitempty" json:"do_action,omitempty"`
	Priority        int           `bson:"priority,omitempty" json:"priority,omitempty"`
	models.SoftDeletes `bson:"timestamps,omitempty" json:"timestamps,omitempty"`
}

func (t *Trigger) ReadyMongoStore() []interface{} {
	var individuals []interface{}
	if t.DoAction != nil {
		individuals = append(individuals, t.DoAction.ReadyMongoStore()...)
	}
	trigger := *t
	trigger.DoAction = nil
	individuals = append(individuals, trigger)
	return individuals
}

func (t *Trigger) GetIdHex() string {
	return t.Id.Hex()
}

func (t *Trigger) GetId() bson.ObjectId {
	return t.Id
}

// Action represents what happens when a trigger fires.
type Action struct {
	Id                      bson.ObjectId     `bson:"_id" json:"id,omitempty"`
	ActionName              string            `bson:"action_name" json:"action_name,omitempty"`
	ExtraActions            []string          `bson:"extra_actions,omitempty" json:"extra_actions,omitempty"`
	BadgeTransaction        *BadgeTransaction `bson:"badge_transaction,omitempty" json:"badge_transaction,omitempty"`
	AdvanceToNextStoryline  bool              `bson:"advance_to_next_storyline,omitempty" json:"advance_to_next_storyline,omitempty"`
	models.SoftDeletes `bson:"timestamps,omitempty" json:"timestamps,omitempty"`
}

func (a *Action) ReadyMongoStore() []interface{} {
	var individuals []interface{}
	if a.BadgeTransaction != nil {
		individuals = append(individuals, a.BadgeTransaction.ReadyMongoStore()...)
	}
	action := *a
	if action.BadgeTransaction != nil {
		action.BadgeTransaction.GiveBadges = nil
		action.BadgeTransaction.RemoveBadges = nil
	}
	individuals = append(individuals, action)
	return individuals
}

func (a *Action) GetIdHex() string {
	return a.Id.Hex()
}

func (a *Action) GetId() bson.ObjectId {
	return a.Id
}

// OutboundWebhook stores webhook configuration for external integrations.
type OutboundWebhook struct {
	Id           bson.ObjectId `bson:"_id" json:"id,omitempty"`
	PublicId     string        `bson:"public_id" json:"public_id,omitempty"`
	SubscriberId string        `bson:"subscriber_id" json:"subscriber_id,omitempty"`
	URL          string        `bson:"url" json:"url,omitempty"`
	Events       []string      `bson:"events,omitempty" json:"events,omitempty"`
	Active       bool          `bson:"active" json:"active,omitempty"`
	models.SoftDeletes `bson:"timestamps,omitempty" json:"timestamps,omitempty"`
}

func NewOutboundWebhook() *OutboundWebhook {
	return &OutboundWebhook{
		Id:       bson.NewObjectId(),
		PublicId: "",
		Active:   true,
	}
}
