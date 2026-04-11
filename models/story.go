package models

import (
	"encoding/json"
	"log"
	"sort"

	"github.com/josephalai/sentanyl/pkg/db"
	"github.com/josephalai/sentanyl/pkg/models"
	"github.com/josephalai/sentanyl/pkg/utils"
	"gopkg.in/mgo.v2/bson"
)

// Story is the root container for email storylines.
type Story struct {
	Id           bson.ObjectId `bson:"_id" json:"id,omitempty"`
	PublicId     string        `bson:"public_id" json:"public_id,omitempty"`
	TenantID     bson.ObjectId `bson:"tenant_id,omitempty" json:"tenant_id,omitempty"`
	SubscriberId string        `bson:"subscriber_id" json:"subscriber_id,omitempty"`
	CreatorId    bson.ObjectId `bson:"creator_id,omitempty" json:"creator_id,omitempty"`
	Name         string        `bson:"name" json:"name,omitempty"`
	StorylineIds *BsonRefIds   `bson:"storyline_ids" json:"-"`
	Storylines   []*Storyline  `bson:"storylines,omitempty" json:"storylines,omitempty"`

	Priority          int  `bson:"priority,omitempty" json:"priority,omitempty"`
	AllowInterruption bool `bson:"allow_interruption,omitempty" json:"allow_interruption,omitempty"`

	OnComplete struct {
		BadgeTransaction *BadgeTransaction `bson:"badge_transaction,omitempty" json:"badge_transaction,omitempty"`
		NextStory        *Story            `bson:"next_story,omitempty" json:"next_story,omitempty"`
		NextStoryId      *BsonRefId        `bson:"next_story_id,omitempty" json:"next_story_id,omitempty"`
	} `bson:"on_complete,omitempty" json:"on_complete,omitempty"`

	OnFail struct {
		BadgeTransaction *BadgeTransaction `bson:"badge_transaction,omitempty" json:"badge_transaction,omitempty"`
		NextStory        *Story            `bson:"next_story,omitempty" json:"next_story,omitempty"`
		NextStoryId      *BsonRefId        `bson:"next_story_id,omitempty" json:"next_story_id,omitempty"`
	} `bson:"on_fail,omitempty" json:"on_fail,omitempty"`

	OnBegin struct {
		BadgeTransaction *BadgeTransaction `bson:"badge_transaction,omitempty" json:"badge_transaction,omitempty"`
	} `bson:"on_begin,omitempty" json:"on_begin,omitempty"`

	RequiredUserBadges struct {
		MustHave    []*RequiredBadge `bson:"must_have,omitempty" json:"must_have,omitempty"`
		MustNotHave []*RequiredBadge `bson:"must_not_have,omitempty" json:"must_not_have,omitempty"`
	} `json:"required_user_badges,omitempty" bson:"required_user_badges,omitempty"`

	StartTrigger    *RequiredBadge `bson:"start_trigger,omitempty" json:"start_trigger,omitempty"`
	CompleteTrigger *RequiredBadge `bson:"complete_trigger,omitempty" json:"complete_trigger,omitempty"`

	models.SoftDeletes `bson:"timestamps,omitempty" json:"timestamps,omitempty"`
}

func NewStory() *Story {
	return &Story{
		Id:         bson.NewObjectId(),
		PublicId:   utils.GeneratePublicId(),
		Storylines: make([]*Storyline, 0),
	}
}

func (s *Story) Hydrate() {
	if s.StorylineIds != nil {
		ids := s.StorylineIds
		s.Storylines = nil
		for _, id := range ids.Ids {
			out := Storyline{}
			db.GetCollection(ids.CollectionName).FindId(id).One(&out)
			s.Storylines = append(s.Storylines, &out)
			out.Hydrate()
		}
	}
	if s.OnComplete.NextStoryId != nil {
		s.OnComplete.NextStory = &Story{}
	}
	if s.OnFail.NextStoryId != nil {
		s.OnFail.NextStory = &Story{}
	}
	if s.OnComplete.BadgeTransaction != nil {
		s.OnComplete.BadgeTransaction.Hydrate()
	}
	if s.OnFail.BadgeTransaction != nil {
		s.OnFail.BadgeTransaction.Hydrate()
	}
	if s.OnBegin.BadgeTransaction != nil {
		s.OnBegin.BadgeTransaction.Hydrate()
	}
	if s.RequiredUserBadges.MustHave != nil {
		for _, badge := range s.RequiredUserBadges.MustHave {
			badge.Hydrate()
		}
		for _, badge := range s.RequiredUserBadges.MustNotHave {
			badge.Hydrate()
		}
	}
	s.Sort()
	log.Println("Done Hydrating Story")
}

func (s *Story) ReadyMongoStore() []interface{} {
	var individuals []interface{}

	s.StorylineIds = NewBsonRefIds()
	s.Sort()

	for _, storyline := range s.Storylines {
		s.StorylineIds.CollectionName = models.StorylineCollection
		s.StorylineIds.Ids = append(s.StorylineIds.Ids, storyline.Id)
		individuals = append(individuals, storyline.ReadyMongoStore()...)
	}

	if s.OnFail.NextStory != nil {
		s.OnFail.NextStoryId = NewBsonRefIdVals(models.StoryCollection, s.OnFail.NextStory.Id)
		individuals = append(individuals, s.OnFail.NextStory.ReadyMongoStore()...)
	}
	if s.OnComplete.NextStory != nil {
		s.OnComplete.NextStoryId = NewBsonRefIdVals(models.StoryCollection, s.OnComplete.NextStory.Id)
		individuals = append(individuals, s.OnComplete.NextStory.ReadyMongoStore()...)
	}

	if s.OnComplete.BadgeTransaction != nil {
		individuals = append(individuals, s.OnComplete.BadgeTransaction.ReadyMongoStore()...)
	}
	if s.OnFail.BadgeTransaction != nil {
		individuals = append(individuals, s.OnFail.BadgeTransaction.ReadyMongoStore()...)
	}
	if s.OnBegin.BadgeTransaction != nil {
		individuals = append(individuals, s.OnBegin.BadgeTransaction.ReadyMongoStore()...)
	}

	if s.RequiredUserBadges.MustHave != nil {
		for _, required := range s.RequiredUserBadges.MustHave {
			individuals = append(individuals, required.ReadyMongoStore()...)
		}
	}
	if s.RequiredUserBadges.MustNotHave != nil {
		for _, required := range s.RequiredUserBadges.MustNotHave {
			individuals = append(individuals, required.ReadyMongoStore()...)
		}
	}

	story := *s
	story.Storylines = nil
	story.OnComplete.NextStory = nil
	story.OnFail.NextStory = nil
	if story.OnBegin.BadgeTransaction != nil {
		story.OnBegin.BadgeTransaction.GiveBadges = nil
	}
	if story.OnFail.BadgeTransaction != nil {
		story.OnFail.BadgeTransaction.RemoveBadges = nil
		story.OnFail.BadgeTransaction.GiveBadges = nil
	}
	if story.OnComplete.BadgeTransaction != nil {
		story.OnComplete.BadgeTransaction.RemoveBadges = nil
		story.OnComplete.BadgeTransaction.GiveBadges = nil
	}

	requiredUserBadges := story.RequiredUserBadges
	if requiredUserBadges.MustNotHave != nil {
		for _, v := range requiredUserBadges.MustNotHave {
			v.Badge = nil
		}
	}
	if requiredUserBadges.MustHave != nil {
		for _, v := range requiredUserBadges.MustHave {
			v.Badge = nil
		}
	}
	individuals = append(individuals, story)
	return individuals
}

func (s *Story) Sort() {
	if s.Storylines != nil {
		sort.Slice(s.Storylines, func(i, j int) bool {
			return s.Storylines[i].NaturalOrder < s.Storylines[j].NaturalOrder
		})
	}
}

func (s *Story) GetIdHex() string {
	return s.Id.Hex()
}

func (s *Story) GetId() bson.ObjectId {
	return s.Id
}

func (s *Story) ToJson() []byte {
	b, err := json.Marshal(s)
	if err != nil {
		log.Printf("Story.ToJson error: %v", err)
	}
	return b
}

func (s *Story) FromJson(jsonData []byte) {
	err := json.Unmarshal(jsonData, s)
	if err != nil {
		log.Printf("Story.FromJson error: %v", err)
	}
}

// Storyline is a collection of enactments within a story.
type Storyline struct {
	Id           bson.ObjectId `bson:"_id" json:"_id,omitempty"`
	PublicId     string        `bson:"public_id" json:"public_id,omitempty"`
	SubscriberId string        `bson:"subscriber_id" json:"subscriber_id,omitempty"`
	CreatorId    bson.ObjectId `bson:"creator_id,omitempty" json:"creator_id,omitempty"`
	Name         string        `bson:"name" json:"name,omitempty"`
	NaturalOrder int           `bson:"natural_order,omitempty" json:"natural_order,omitempty"`
	ActIds       *BsonRefIds   `bson:"act_ids,omitempty" json:"-"`
	Acts         []*Enactment  `bson:"acts,omitempty" json:"enactments,omitempty"`

	RequiredUserBadges struct {
		MustHave    []*RequiredBadge `bson:"must_have,omitempty" json:"must_have,omitempty"`
		MustNotHave []*RequiredBadge `bson:"must_not_have,omitempty" json:"must_not_have,omitempty"`
	} `bson:"required_user_badges,omitempty" json:"required_user_badges,omitempty"`

	OnComplete struct {
		BadgeTransaction *BadgeTransaction `bson:"badge_transaction,omitempty" json:"badge_transaction,omitempty"`
		NextStoryline    *Storyline        `bson:"next_storyline,omitempty" json:"next_storyline,omitempty"`
		NextStorylineId  *BsonRefId        `bson:"next_storyline_id,omitempty" json:"next_storyline_id,omitempty"`
	} `bson:"on_complete_begin,omitempty" json:"on_complete_begin,omitempty"`

	OnFail struct {
		BadgeTransaction *BadgeTransaction `bson:"badge_transaction,omitempty" json:"badge_transaction,omitempty"`
		NextStoryline    *Storyline        `bson:"next_storyline,omitempty" json:"next_storyline,omitempty"`
		NextStorylineId  *BsonRefId        `bson:"next_storyline_id,omitempty" json:"next_storyline_id,omitempty"`
	} `bson:"on_fail_begin,omitempty" json:"on_fail_begin,omitempty"`

	OnBegin struct {
		BadgeTransaction *BadgeTransaction `bson:"badge_transaction,omitempty" json:"badge_transaction,omitempty"`
	} `bson:"on_begin,omitempty" json:"on_begin,omitempty"`

	models.SoftDeletes `bson:"timestamps,omitempty" json:"timestamps,omitempty"`
}

func NewStoryline() *Storyline {
	return &Storyline{
		Id:       bson.NewObjectId(),
		PublicId: utils.GeneratePublicId(),
		Acts:     []*Enactment{},
	}
}

func (s *Storyline) Hydrate() {
	if s.ActIds != nil {
		ids := s.ActIds
		s.Acts = nil
		for _, id := range ids.Ids {
			out := Enactment{}
			db.GetCollection(ids.CollectionName).FindId(id).One(&out)
			s.Acts = append(s.Acts, &out)
			out.Hydrate()
		}
	}
	if s.OnComplete.NextStorylineId != nil {
		s.OnComplete.NextStoryline = &Storyline{}
		db.GetCollection(s.OnComplete.NextStorylineId.CollectionName).FindId(s.OnComplete.NextStorylineId.Id).One(s.OnComplete.NextStoryline)
	}
	if s.OnFail.NextStorylineId != nil {
		s.OnFail.NextStoryline = &Storyline{}
		db.GetCollection(s.OnFail.NextStorylineId.CollectionName).FindId(s.OnFail.NextStorylineId.Id).One(s.OnFail.NextStoryline)
	}
	if s.OnComplete.BadgeTransaction != nil {
		s.OnComplete.BadgeTransaction.Hydrate()
	}
	if s.OnFail.BadgeTransaction != nil {
		s.OnFail.BadgeTransaction.Hydrate()
	}
	if s.OnBegin.BadgeTransaction != nil {
		s.OnBegin.BadgeTransaction.Hydrate()
	}
	for _, rb := range s.RequiredUserBadges.MustHave {
		rb.Hydrate()
	}
	for _, rb := range s.RequiredUserBadges.MustNotHave {
		rb.Hydrate()
	}
	s.Sort()
}

func (s *Storyline) ReadyMongoStore() []interface{} {
	var individuals []interface{}

	s.ActIds = NewBsonRefIds()
	s.Sort()

	for _, enactment := range s.Acts {
		s.ActIds.CollectionName = models.EnactmentCollection
		s.ActIds.Ids = append(s.ActIds.Ids, enactment.Id)
		individuals = append(individuals, enactment.ReadyMongoStore()...)
	}

	if s.OnFail.NextStoryline != nil {
		s.OnFail.NextStorylineId = NewBsonRefIdVals(models.StorylineCollection, s.OnFail.NextStoryline.Id)
		individuals = append(individuals, s.OnFail.NextStoryline.ReadyMongoStore()...)
	}
	if s.OnComplete.NextStoryline != nil {
		s.OnComplete.NextStorylineId = NewBsonRefIdVals(models.StorylineCollection, s.OnComplete.NextStoryline.Id)
		individuals = append(individuals, s.OnComplete.NextStoryline.ReadyMongoStore()...)
	}

	if s.OnComplete.BadgeTransaction != nil {
		individuals = append(individuals, s.OnComplete.BadgeTransaction.ReadyMongoStore()...)
	}
	if s.OnFail.BadgeTransaction != nil {
		individuals = append(individuals, s.OnFail.BadgeTransaction.ReadyMongoStore()...)
	}
	if s.OnBegin.BadgeTransaction != nil {
		individuals = append(individuals, s.OnBegin.BadgeTransaction.ReadyMongoStore()...)
	}

	for _, rb := range s.RequiredUserBadges.MustHave {
		individuals = append(individuals, rb.ReadyMongoStore()...)
	}
	for _, rb := range s.RequiredUserBadges.MustNotHave {
		individuals = append(individuals, rb.ReadyMongoStore()...)
	}

	sl := *s
	sl.Acts = nil
	sl.OnFail.NextStoryline = nil
	sl.OnComplete.NextStoryline = nil
	if sl.OnBegin.BadgeTransaction != nil {
		sl.OnBegin.BadgeTransaction.GiveBadges = nil
	}
	if sl.OnComplete.BadgeTransaction != nil {
		sl.OnComplete.BadgeTransaction.GiveBadges = nil
		sl.OnComplete.BadgeTransaction.RemoveBadges = nil
	}
	if sl.OnFail.BadgeTransaction != nil {
		sl.OnFail.BadgeTransaction.GiveBadges = nil
		sl.OnFail.BadgeTransaction.RemoveBadges = nil
	}
	for _, rb := range sl.RequiredUserBadges.MustHave {
		rb.Badge = nil
	}
	for _, rb := range sl.RequiredUserBadges.MustNotHave {
		rb.Badge = nil
	}
	individuals = append(individuals, sl)
	return individuals
}

func (s *Storyline) Sort() {
	if s.Acts != nil {
		sort.Slice(s.Acts, func(i, j int) bool {
			return s.Acts[i].NaturalOrder < s.Acts[j].NaturalOrder
		})
	}
}

func (s *Storyline) GetIdHex() string {
	return s.Id.Hex()
}

func (s *Storyline) GetId() bson.ObjectId {
	return s.Id
}

// Enactment is a segment within a storyline (email scene + triggers).
type Enactment struct {
	Id               bson.ObjectId     `bson:"_id" json:"_id,omitempty"`
	PublicId         string            `bson:"public_id" json:"public_id,omitempty"`
	SubscriberId     string            `bson:"subscriber_id" json:"subscriber_id,omitempty"`
	CreatorId        bson.ObjectId     `bson:"creator_id,omitempty" json:"creator_id,omitempty"`
	Name             string            `bson:"name" json:"name,omitempty"`
	Level            int               `bson:"level" json:"level,omitempty"`
	NaturalOrder     int               `bson:"natural_order" json:"natural_order,omitempty"`
	BadgeTransaction *BadgeTransaction `bson:"badge_transaction,omitempty" json:"badge_transaction,omitempty"`
	SendSceneId      *BsonRefId        `bson:"next_storyline_id,omitempty" json:"next_storyline_id,omitempty"`
	SendScene        *Scene            `bson:"send_scene,omitempty" json:"send_scene,omitempty"`
	SendScenesIds    *BsonRefIds       `bson:"send_scenes_ids,omitempty" json:"-"`
	SendScenes       []*Scene          `bson:"send_scenes,omitempty" json:"send_scenes,omitempty"`

	SkipToNextStorylineOnExpiry bool `bson:"skip_storyline_on_expiry,omitempty" json:"skip_storyline_on_expiry,omitempty"`

	OnEventIds *BsonRefIds            `bson:"trigger_ids,omitempty" json:"-"`
	OnEvent    map[string][]*Trigger  `bson:"trigger,omitempty" json:"trigger,omitempty"`

	models.SoftDeletes `bson:"timestamps,omitempty" json:"timestamps,omitempty"`
}

func NewEnactment() *Enactment {
	return &Enactment{
		Id:       bson.NewObjectId(),
		PublicId: utils.GeneratePublicId(),
		OnEvent:  map[string][]*Trigger{},
	}
}

func (e *Enactment) Hydrate() {
	if e.OnEventIds != nil {
		e.OnEvent = map[string][]*Trigger{}
		ids := e.OnEventIds
		for _, id := range ids.Ids {
			out := Trigger{}
			db.GetCollection(ids.CollectionName).FindId(id).One(&out)
			e.OnEvent[out.TriggerType] = append(e.OnEvent[out.TriggerType], &out)
		}
	}
	if e.OnEvent == nil {
		e.OnEvent = map[string][]*Trigger{}
	}
	if e.SendSceneId != nil {
		e.SendScene = &Scene{}
		db.GetCollection(e.SendSceneId.CollectionName).FindId(e.SendSceneId.Id).One(e.SendScene)
		e.SendScene.Hydrate()
	}
	if e.SendScenesIds != nil && len(e.SendScenesIds.Ids) > 0 {
		e.SendScenes = make([]*Scene, 0, len(e.SendScenesIds.Ids))
		for _, id := range e.SendScenesIds.Ids {
			sc := &Scene{}
			db.GetCollection(e.SendScenesIds.CollectionName).FindId(id).One(sc)
			sc.Hydrate()
			e.SendScenes = append(e.SendScenes, sc)
		}
	}
}

func (e *Enactment) ReadyMongoStore() []interface{} {
	var individuals []interface{}

	if e.SendScene != nil && len(e.SendScenes) == 0 {
		e.SendSceneId = NewBsonRefIdVals(models.SceneCollection, e.SendScene.Id)
		individuals = append(individuals, e.SendScene.ReadyMongoStore()...)
	}
	if len(e.SendScenes) > 0 {
		sceneIds := make([]bson.ObjectId, 0, len(e.SendScenes))
		for _, sc := range e.SendScenes {
			if sc != nil {
				sceneIds = append(sceneIds, sc.Id)
				individuals = append(individuals, sc.ReadyMongoStore()...)
			}
		}
		e.SendScenesIds = &BsonRefIds{CollectionName: models.SceneCollection, Ids: sceneIds}
	}

	if e.OnEvent != nil {
		allIds := []bson.ObjectId{}
		for _, triggers := range e.OnEvent {
			for _, t := range triggers {
				if t != nil {
					allIds = append(allIds, t.Id)
					individuals = append(individuals, t.ReadyMongoStore()...)
				}
			}
		}
		e.OnEventIds = &BsonRefIds{CollectionName: models.TriggerCollection, Ids: allIds}
	}

	if e.BadgeTransaction != nil {
		individuals = append(individuals, e.BadgeTransaction.ReadyMongoStore()...)
	}

	act := *e
	act.OnEvent = nil
	act.SendScene = nil
	act.SendScenes = nil
	if e.BadgeTransaction != nil {
		e.BadgeTransaction.GiveBadges = nil
		e.BadgeTransaction.RemoveBadges = nil
	}
	individuals = append(individuals, act)
	return individuals
}

func (e *Enactment) SceneCount() int {
	if len(e.SendScenes) > 0 {
		return len(e.SendScenes)
	}
	if e.SendScene != nil {
		return 1
	}
	return 0
}

func (e *Enactment) GetScene(idx int) *Scene {
	if len(e.SendScenes) > 0 {
		if idx >= 0 && idx < len(e.SendScenes) {
			return e.SendScenes[idx]
		}
		return nil
	}
	if idx == 0 {
		return e.SendScene
	}
	return nil
}

func (e *Enactment) GetIdHex() string {
	return e.Id.Hex()
}

func (e *Enactment) GetId() bson.ObjectId {
	return e.Id
}

// Scene represents an email scene within an enactment.
type Scene struct {
	Id           bson.ObjectId `bson:"_id,omitempty" json:"id,omitempty"`
	PublicId     string        `bson:"public_id" json:"public_id,omitempty"`
	SubscriberId string        `bson:"subscriber_id" json:"subscriber_id,omitempty"`
	CreatorId    bson.ObjectId `bson:"creator_id,omitempty" json:"creator_id,omitempty"`
	Name         string        `bson:"name" json:"name,omitempty"`
	MessageId    *BsonRefId    `bson:"message_id,omitempty" json:"message_id,omitempty"`
	Message      *Message      `bson:"message,omitempty" json:"message,omitempty"`
	TagsIds      *BsonRefIds   `bson:"tags_ids,omitempty" json:"-"`
	Tags         []*Tag        `bson:"tags,omitempty" json:"tags,omitempty"`

	Subject   string            `bson:"subject,omitempty" json:"subject,omitempty"`
	Body      string            `bson:"body,omitempty" json:"body,omitempty"`
	FromEmail string            `bson:"from_email,omitempty" json:"from_email,omitempty"`
	FromName  string            `bson:"from_name,omitempty" json:"from_name,omitempty"`
	ReplyTo   string            `bson:"reply_to,omitempty" json:"reply_to,omitempty"`
	Vars      map[string]string `bson:"vars,omitempty" json:"vars,omitempty"`

	models.SoftDeletes `bson:"timestamps,omitempty" json:"timestamps,omitempty"`
}

func NewScene() *Scene {
	return &Scene{
		Id:       bson.NewObjectId(),
		PublicId: utils.GeneratePublicId(),
	}
}

func (s *Scene) Hydrate() {
	if s.MessageId != nil {
		s.Message = &Message{}
		db.GetCollection(s.MessageId.CollectionName).FindId(s.MessageId.Id).One(s.Message)
		s.Message.Hydrate()
	} else if s.Message != nil {
		s.Message.Hydrate()
	}
	s.Tags = []*Tag{}
	if s.TagsIds != nil {
		for _, id := range s.TagsIds.Ids {
			out := Tag{}
			db.GetCollection(s.TagsIds.CollectionName).FindId(id).One(&out)
			s.Tags = append(s.Tags, &out)
		}
	}
}

func (s *Scene) ReadyMongoStore() []interface{} {
	var individuals []interface{}

	if s.Message != nil {
		s.MessageId = NewBsonRefIdVals(models.MessageCollection, s.Message.Id)
		individuals = append(individuals, s.Message.ReadyMongoStore()...)
	}

	if s.Tags != nil {
		tagIds := []bson.ObjectId{}
		for _, tag := range s.Tags {
			tagIds = append(tagIds, tag.Id)
			individuals = append(individuals, tag.ReadyMongoStore()...)
		}
		s.TagsIds = &BsonRefIds{CollectionName: models.TagCollection, Ids: tagIds}
	}

	sc := *s
	sc.Message = nil
	sc.Tags = nil
	individuals = append(individuals, sc)
	return individuals
}

func (s *Scene) GetIdHex() string {
	return s.Id.Hex()
}

func (s *Scene) GetId() bson.ObjectId {
	return s.Id
}

// Message contains email message content.
type Message struct {
	Id        bson.ObjectId  `bson:"_id,omitempty" json:"id,omitempty"`
	PublicId  string         `bson:"public_id" json:"public_id,omitempty"`
	ContentId *BsonRefId     `bson:"content_id,omitempty" json:"content_id,omitempty"`
	Content   *MessageContent `bson:"content,omitempty" json:"content,omitempty"`
	models.SoftDeletes `bson:"timestamps,omitempty" json:"timestamps,omitempty"`
}

func (m *Message) Hydrate() {
	if m.ContentId != nil {
		m.Content = &MessageContent{}
		db.GetCollection(m.ContentId.CollectionName).FindId(m.ContentId.Id).One(m.Content)
	}
}

func (m *Message) ReadyMongoStore() []interface{} {
	var individuals []interface{}
	if m.Content != nil {
		m.ContentId = NewBsonRefIdVals(models.MessageContentCollection, m.Content.Id)
		individuals = append(individuals, *m.Content)
	}
	msg := *m
	msg.Content = nil
	individuals = append(individuals, msg)
	return individuals
}

func (m *Message) GetIdHex() string {
	return m.Id.Hex()
}

func (m *Message) GetId() bson.ObjectId {
	return m.Id
}

// MessageContent holds the actual email fields.
type MessageContent struct {
	Id           bson.ObjectId     `bson:"_id,omitempty" json:"id,omitempty"`
	PublicId     string            `bson:"public_id" json:"public_id,omitempty"`
	SubjectLine  string            `bson:"subject_line" json:"subject_line,omitempty"`
	From         string            `bson:"from" json:"from,omitempty"`
	ReplyTo      string            `bson:"reply_to,omitempty" json:"reply_to,omitempty"`
	Body         string            `bson:"body,omitempty" json:"body,omitempty"`
	TemplateVars map[string]string `bson:"template_vars,omitempty" json:"template_vars,omitempty"`
	models.SoftDeletes `bson:"timestamps,omitempty" json:"timestamps,omitempty"`
}

func (mc *MessageContent) GetIdHex() string {
	return mc.Id.Hex()
}

func (mc *MessageContent) GetId() bson.ObjectId {
	return mc.Id
}

// Tag is a metadata tag.
type Tag struct {
	Id       bson.ObjectId `bson:"_id,omitempty" json:"id,omitempty"`
	PublicId string        `bson:"public_id" json:"public_id,omitempty"`
	Name     string        `bson:"name" json:"name,omitempty"`
	Value    string        `bson:"value,omitempty" json:"value,omitempty"`
	models.SoftDeletes `bson:"timestamps,omitempty" json:"timestamps,omitempty"`
}

func (t *Tag) ReadyMongoStore() []interface{} {
	return []interface{}{*t}
}

func (t *Tag) GetIdHex() string {
	return t.Id.Hex()
}

func (t *Tag) GetId() bson.ObjectId {
	return t.Id
}

// StoryStandalone is a simplified read-only view of a Story used for listing.
type StoryStandalone struct {
	Id       bson.ObjectId `bson:"_id" json:"id,omitempty"`
	PublicId string        `bson:"public_id" json:"public_id,omitempty"`
	Name     string        `bson:"name" json:"name,omitempty"`
	models.SoftDeletes `bson:"timestamps,omitempty" json:"timestamps,omitempty"`
}

func (ss *StoryStandalone) GetIdHex() string {
	return ss.Id.Hex()
}

func (ss *StoryStandalone) GetId() bson.ObjectId {
	return ss.Id
}
