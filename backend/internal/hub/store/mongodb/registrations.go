package mongodb

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
)

func regToDoc(p store.CreateRegistrationParams, now time.Time) bson.D {
	return bson.D{
		{Key: "_id", Value: p.ID},
		{Key: "version", Value: p.Version},
		{Key: "public_key", Value: bytesVal(p.PublicKey)},
		{Key: "mlkem_public_key", Value: bytesVal(p.MlkemPublicKey)},
		{Key: "slhdsa_public_key", Value: bytesVal(p.SlhdsaPublicKey)},
		{Key: "status", Value: int32(leapmuxv1.RegistrationStatus_REGISTRATION_STATUS_PENDING)},
		{Key: "expires_at", Value: truncateMS(p.ExpiresAt)},
		{Key: "created_at", Value: now},
	}
}

func docToReg(m bson.M) store.WorkerRegistration {
	return store.WorkerRegistration{
		ID:              getS(m, "_id"),
		Version:         getS(m, "version"),
		PublicKey:       getBytes(m, "public_key"),
		MlkemPublicKey:  getBytes(m, "mlkem_public_key"),
		SlhdsaPublicKey: getBytes(m, "slhdsa_public_key"),
		Status:          leapmuxv1.RegistrationStatus(getInt32(m, "status")),
		WorkerID:        ptrconv.StringToPtr(getS(m, "worker_id")),
		ApprovedBy:      ptrconv.StringToPtr(getS(m, "approved_by")),
		ExpiresAt:       getTime(m, "expires_at"),
		CreatedAt:       getTime(m, "created_at"),
	}
}

func (st *registrationStore) Create(ctx context.Context, p store.CreateRegistrationParams) error {
	now := truncateMS(time.Now().UTC())
	doc := regToDoc(p, now)
	_, err := st.s.collection(colRegistrations).InsertOne(ctx, doc)
	if err != nil {
		return mapErr(err)
	}
	st.s.trackInsert(colRegistrations, p.ID)
	return nil
}

func (st *registrationStore) GetByID(ctx context.Context, id string) (*store.WorkerRegistration, error) {
	filter := bson.D{{Key: "_id", Value: id}}
	var m bson.M
	err := st.s.collection(colRegistrations).FindOne(ctx, filter).Decode(&m)
	if err != nil {
		return nil, mapErr(err)
	}
	r := docToReg(m)
	return &r, nil
}

func (st *registrationStore) Approve(ctx context.Context, p store.ApproveRegistrationParams) error {
	filter := bson.D{
		{Key: "_id", Value: p.ID},
		{Key: "status", Value: int32(leapmuxv1.RegistrationStatus_REGISTRATION_STATUS_PENDING)},
	}
	setFields := bson.D{
		{Key: "status", Value: int32(leapmuxv1.RegistrationStatus_REGISTRATION_STATUS_APPROVED)},
	}
	if p.WorkerID != nil {
		setFields = append(setFields, bson.E{Key: "worker_id", Value: *p.WorkerID})
	}
	if p.ApprovedBy != nil {
		setFields = append(setFields, bson.E{Key: "approved_by", Value: *p.ApprovedBy})
	}
	update := bson.D{
		{Key: "$set", Value: setFields},
	}
	_, err := st.s.collection(colRegistrations).UpdateOne(ctx, filter, update)
	return mapErr(err)
}

func (st *registrationStore) ExpirePending(ctx context.Context) error {
	now := truncateMS(time.Now().UTC())
	filter := bson.D{
		{Key: "status", Value: int32(leapmuxv1.RegistrationStatus_REGISTRATION_STATUS_PENDING)},
		{Key: "expires_at", Value: bson.D{{Key: "$lt", Value: now}}},
	}
	update := bson.D{
		{Key: "$set", Value: bson.D{
			{Key: "status", Value: int32(leapmuxv1.RegistrationStatus_REGISTRATION_STATUS_EXPIRED)},
		}},
	}
	_, err := st.s.collection(colRegistrations).UpdateMany(ctx, filter, update)
	return mapErr(err)
}
