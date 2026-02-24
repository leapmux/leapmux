package service_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/db"
	gendb "github.com/leapmux/leapmux/internal/hub/generated/db"
	"github.com/leapmux/leapmux/internal/hub/id"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/timeout"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
)

type fileTestEnv struct {
	client    leapmuxv1connect.FileServiceClient
	queries   *gendb.Queries
	workerMgr *workermgr.Manager
	token     string
	orgID     string
	userID    string
	workerID  string
}

func setupFileTest(t *testing.T) *fileTestEnv {
	t.Helper()

	sqlDB, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	err = db.Migrate(sqlDB)
	require.NoError(t, err)

	queries := gendb.New(sqlDB)
	workerMgr := workermgr.New()

	tc, tcErr := timeout.NewFromDB(queries)
	require.NoError(t, tcErr)

	pendingReqs := workermgr.NewPendingRequests(tc.APITimeout)

	fileSvc := service.NewFileService(queries, workerMgr, pendingReqs)

	mux := http.NewServeMux()
	opts := connect.WithInterceptors(auth.NewInterceptor(queries))
	path, handler := leapmuxv1connect.NewFileServiceHandler(fileSvc, opts)
	mux.Handle(path, handler)

	server := httptest.NewUnstartedServer(mux)
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)

	client := leapmuxv1connect.NewFileServiceClient(
		server.Client(),
		server.URL,
		connect.WithGRPC(),
	)

	orgID := id.Generate()
	userID := id.Generate()
	hash, _ := bcrypt.GenerateFromPassword([]byte("pass"), bcrypt.MinCost)

	_ = queries.CreateOrg(context.Background(), gendb.CreateOrgParams{ID: orgID, Name: "test-org"})
	_ = queries.CreateUser(context.Background(), gendb.CreateUserParams{
		ID:           userID,
		OrgID:        orgID,
		Username:     "testuser",
		PasswordHash: string(hash),
		DisplayName:  "Test",
		IsAdmin:      1,
	})

	token, _, err := auth.Login(context.Background(), queries, "testuser", "pass")
	require.NoError(t, err)

	workerID := id.Generate()
	_ = queries.CreateWorker(context.Background(), gendb.CreateWorkerParams{
		ID:           workerID,
		OrgID:        orgID,
		Name:         "test-worker",
		Hostname:     "localhost",
		AuthToken:    id.Generate(),
		RegisteredBy: userID,
	})

	return &fileTestEnv{
		client:    client,
		queries:   queries,
		workerMgr: workerMgr,
		token:     token,
		orgID:     orgID,
		userID:    userID,
		workerID:  workerID,
	}
}

func TestFileService_ListDirectory_MissingWorkerID(t *testing.T) {
	env := setupFileTest(t)

	_, err := env.client.ListDirectory(context.Background(), authedReq(&leapmuxv1.ListDirectoryRequest{
		Path: "/home",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestFileService_ListDirectory_WorkerNotFound(t *testing.T) {
	env := setupFileTest(t)

	_, err := env.client.ListDirectory(context.Background(), authedReq(&leapmuxv1.ListDirectoryRequest{
		WorkerId: "nonexistent",
		Path:     "/home",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestFileService_ListDirectory_WorkerOffline(t *testing.T) {
	env := setupFileTest(t)

	// Worker exists in DB but is not registered (offline).
	_, err := env.client.ListDirectory(context.Background(), authedReq(&leapmuxv1.ListDirectoryRequest{
		WorkerId: env.workerID,
		Path:     "/home",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}

func TestFileService_ReadFile_MissingWorkerID(t *testing.T) {
	env := setupFileTest(t)

	_, err := env.client.ReadFile(context.Background(), authedReq(&leapmuxv1.ReadFileRequest{
		Path: "/home/test.txt",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestFileService_StatFile_MissingWorkerID(t *testing.T) {
	env := setupFileTest(t)

	_, err := env.client.StatFile(context.Background(), authedReq(&leapmuxv1.StatFileRequest{
		Path: "/home/test.txt",
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestFileService_Unauthenticated(t *testing.T) {
	env := setupFileTest(t)

	_, err := env.client.ListDirectory(context.Background(), connect.NewRequest(&leapmuxv1.ListDirectoryRequest{
		WorkerId: env.workerID,
		Path:     "/home",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}
