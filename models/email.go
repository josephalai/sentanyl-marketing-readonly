package models

import (
	"encoding/json"
	"log"
	"time"

	"github.com/josephalai/sentanyl/pkg/utils"
	"gopkg.in/mgo.v2/bson"
)

// Email represents a scheduled or instant email record.
type Email struct {
	Id          bson.ObjectId `bson:"_id" json:"id"`
	PublicId    string        `bson:"public_id" json:"public_id"`
	ResponseId  string        `bson:"return_id,omitempty" json:"return_id,omitempty"`
	From        string        `bson:"from" json:"from"`
	To          string        `bson:"to" json:"to"`
	SubjectLine string        `bson:"subject_line" json:"subject_line"`
	ReplyTo     string        `bson:"reply_to" json:"reply_to"`
	VMTA        string        `bson:"vmta,omitempty" json:"vmta,omitempty"`
	Html        string        `bson:"html" json:"html"`
	Scheduled   *time.Time    `bson:"scheduled_time,omitempty" json:"scheduled_time"`
	Created     *time.Time    `bson:"created,omitempty" json:"created"`
	Sent        *time.Time    `bson:"sent,omitempty" json:"sent"`
}

func NewScheduledEmail() *Email {
	t := time.Now()
	return &Email{
		Id:        bson.NewObjectId(),
		PublicId:  utils.GeneratePublicId(),
		Scheduled: &t,
	}
}

func NewInstantEmail() *Email {
	return &Email{
		Id:        bson.NewObjectId(),
		PublicId:  utils.GeneratePublicId(),
		Scheduled: nil,
	}
}

func (s *Email) GetIdHex() string {
	return s.Id.Hex()
}

func (s *Email) GetId() bson.ObjectId {
	return s.Id
}

func (s *Email) ToJson() []byte {
	b, err := json.Marshal(s)
	if err != nil {
		log.Printf("Email.ToJson error: %v", err)
	}
	return b
}

func (s *Email) FromJson(jsonData []byte) {
	err := json.Unmarshal(jsonData, s)
	if err != nil {
		log.Printf("Email.FromJson error: %v", err)
	}
}
