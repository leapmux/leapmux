package service

import (
	"context"
	"strings"
	"sync"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
)

func TestCreationShutdown_CancelsPublicationStorageBeforeJoining(t *testing.T) {
	t.Parallel()
	writeEntered := make(chan struct{})
	var writeOnce sync.Once
	svc, dispatcher, _ := setupTestService(t, withQueryRewrite(func(query string) string {
		if strings.Contains(query, "-- name: CommitTabCreation") {
			writeOnce.Do(func() { close(writeEntered) })
		}
		return query
	}))
	defer svc.creations.stop()
	publishEntered, publishRelease := make(chan struct{}), make(chan struct{})
	var publishOnce, releaseOnce sync.Once
	releasePublish := func() { releaseOnce.Do(func() { close(publishRelease) }) }
	defer releasePublish()
	svc.PublishTab = func(ctx context.Context, request *leapmuxv1.TabPublication) (*leapmuxv1.PublishTabResponse, error) {
		publishOnce.Do(func() { close(publishEntered) })
		select {
		case <-publishRelease:
			return fixturePublishTab(ctx, request)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	method, request := creationRequestForTest(t, leapmuxv1.TabType_TAB_TYPE_FILE, "blocked-publication-write", t.TempDir())
	writer, creatorDone := newTestWriter(), make(chan struct{})
	go func() { defer close(creatorDone); dispatch(dispatcher, method, request, writer) }()
	ctx := creationOutcomeContext(t)
	select {
	case <-publishEntered:
	case <-ctx.Done():
		t.Fatal("creation did not reach publication")
	}
	svc.DB.SetMaxOpenConns(1)
	held, err := svc.DB.Conn(ctx)
	require.NoError(t, err)
	var closeOnce sync.Once
	releaseConnection := func() { closeOnce.Do(func() { assert.NoError(t, held.Close()) }) }
	defer releaseConnection()
	before := svc.DB.Stats().WaitCount
	releasePublish()
	select {
	case <-writeEntered:
	case <-ctx.Done():
		t.Fatal("publication did not reach storage")
	}
	testutil.RequireEventually(t, func() bool { return svc.DB.Stats().WaitCount > before }, "publication storage did not wait for the held connection")
	stopped := make(chan struct{})
	go func() { defer close(stopped); svc.creations.stop() }()
	joinedBeforeRelease := false
	select {
	case <-stopped:
		joinedBeforeRelease = true
	case <-ctx.Done():
		// Release only to clean up a failed test of the old mutex ordering.
		releaseConnection()
		<-stopped
	}
	require.True(t, joinedBeforeRelease, "shutdown must cancel and join the publisher before the connection becomes available")
	select {
	case <-creatorDone:
	case <-ctx.Done():
		t.Fatal("the creator did not join after cancellation")
	}
	require.Len(t, writer.rejections(), 1)
	assert.NotEqual(t, int32(codes.Aborted), writer.rejections()[0].code, "shutdown cannot disprove an earlier Hub commit")
	releaseConnection()
	row, err := svc.Queries.GetTabCreation(t.Context(), db.GetTabCreationParams{UserID: "user-1", WorkerID: svc.WorkerID, TabID: "blocked-publication-write"})
	require.NoError(t, err)
	assert.Equal(t, creationPending, row.State)
	assert.NotEmpty(t, row.RequestPayload)
	assert.NotEmpty(t, row.RuntimePayload)
	_, err = svc.TabPayloads.Get(t.Context(), "user-1", row.TabID)
	assert.NoError(t, err, "unknown publication must preserve the persisted resource")
}
