package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/leapmux/leapmux/internal/hub/store"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

const creationCrashChild = "LEAPMUX_TEST_CREATION_CRASH_CHILD"

// TestCreationRestart_FileProcessHelper runs only in the subprocess below.
func TestCreationRestart_FileProcessHelper(t *testing.T) {
	if os.Getenv(creationCrashChild) != "1" {
		return
	}
	directory := os.Getenv("LEAPMUX_TEST_CREATION_DIRECTORY")
	database := openRecoveryDatabase(t, os.Getenv("LEAPMUX_TEST_CREATION_DATABASE"))
	baseURL := os.Getenv("LEAPMUX_TEST_CREATION_HUB")
	cfg := Config{DB: database, WorkerID: os.Getenv("LEAPMUX_TEST_CREATION_WORKER"), SeedRegisteredBy: os.Getenv("LEAPMUX_TEST_CREATION_OWNER"), HomeDir: directory, DataDir: directory, APITimeout: 30 * time.Second,
		PublishTab: func(ctx context.Context, request *leapmuxv1.TabPublication) (*leapmuxv1.PublishTabResponse, error) {
			payload, err := proto.Marshal(request)
			if err != nil {
				return nil, err
			}
			httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL, bytes.NewReader(payload))
			if err != nil {
				return nil, err
			}
			response, err := http.DefaultClient.Do(httpRequest)
			if err != nil {
				return nil, err
			}
			defer func() { _ = response.Body.Close() }()
			body, err := io.ReadAll(response.Body)
			if err != nil {
				return nil, err
			}
			published := &leapmuxv1.PublishTabResponse{}
			if err := proto.Unmarshal(body, published); err != nil {
				return nil, err
			}
			return published, nil
		},
		ResolveTabPublication: func(context.Context, *leapmuxv1.TabPublication) (*leapmuxv1.ResolveTabPublicationResponse, error) {
			return nil, errors.New("the unacknowledged child must not resolve startup")
		},
		CancelTabPublication: func(context.Context, *leapmuxv1.TabPublication) error {
			return errors.New("the crash child must not cancel creation")
		},
	}
	svc, dispatcher := newRecoveryService(t, cfg)
	intent := &leapmuxv1.TabCreation{TabId: "process-restart-file", Placement: &leapmuxv1.TabPlacement{WorkspaceId: os.Getenv("LEAPMUX_TEST_CREATION_WORKSPACE"), Destination: &leapmuxv1.TabPlacement_TileId{TileId: "root"}, Insertion: leapmuxv1.TabInsertion_TAB_INSERTION_LAST}}
	method, request := recoveryCreationRequest(t, leapmuxv1.TabType_TAB_TYPE_FILE, intent, directory)
	writer := newTestWriter()
	dispatchRecoveryRequest(t.Context(), svc, dispatcher, method, request, writer)
	t.Fatalf("the crash child returned before parent termination: %v", writer.rejections())
}

func TestCreationRestart_RecoversAFileAfterProcessTermination(t *testing.T) {
	t.Parallel()
	hub := newRecoveryPublicationHub(t)
	directory := t.TempDir()
	path := filepath.Join(directory, "worker.db")
	committed := make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request := &leapmuxv1.TabPublication{}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err == nil {
			err = proto.Unmarshal(body, request)
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		response, err := hub.manager.PublishTab(r.Context(), hub.workerID, request)
		if err != nil || response.GetPublished() == nil {
			http.Error(w, "publication did not commit", http.StatusInternalServerError)
			return
		}
		once.Do(func() { close(committed) })
		// The parent terminates the Worker after this durable commit. It never
		// delivers the acknowledgement to the process that created the payload.
		<-r.Context().Done()
	}))
	defer server.Close()
	executable, err := os.Executable()
	require.NoError(t, err)
	ctx := creationOutcomeContext(t)
	child := exec.CommandContext(ctx, executable, "-test.run=^TestCreationRestart_FileProcessHelper$", "-test.count=1")
	child.Env = append(os.Environ(), creationCrashChild+"=1", "LEAPMUX_TEST_CREATION_DATABASE="+path, "LEAPMUX_TEST_CREATION_DIRECTORY="+directory, "LEAPMUX_TEST_CREATION_HUB="+server.URL, "LEAPMUX_TEST_CREATION_OWNER="+hub.owner.String(), "LEAPMUX_TEST_CREATION_WORKER="+hub.workerID, "LEAPMUX_TEST_CREATION_WORKSPACE="+hub.workspaceID)
	var output bytes.Buffer
	child.Stdout, child.Stderr = &output, &output
	require.NoError(t, child.Start())
	finished := make(chan error, 1)
	go func() { finished <- child.Wait() }()
	waited := false
	defer func() {
		if !waited {
			_ = child.Process.Kill()
			assert.Error(t, <-finished, "terminated child cleanup must receive an exit failure")
		}
		server.CloseClientConnections()
	}()
	select {
	case <-committed:
	case err := <-finished:
		waited = true
		t.Fatalf("crash child ended before publication committed: %v\n%s", err, output.String())
	case <-ctx.Done():
		t.Fatal("the child never committed its publication")
	}
	require.NoError(t, child.Process.Kill())
	childErr := <-finished
	waited = true
	var exitError *exec.ExitError
	require.ErrorAs(t, childErr, &exitError, "the parent must stop an unacknowledged running process")
	server.CloseClientConnections()
	database := openRecoveryDatabase(t, path)
	fresh, _ := newRecoveryService(t, hub.config(database, directory))
	defer fresh.Shutdown()
	before, err := fresh.Queries.GetTabCreation(ctx, db.GetTabCreationParams{UserID: hub.owner.String(), WorkerID: hub.workerID, TabID: "process-restart-file"})
	require.NoError(t, err)
	require.Equal(t, creationPending, before.State)
	require.NoError(t, fresh.ResolvePendingTabCreations(ctx))
	row, err := fresh.Queries.GetTabCreation(ctx, db.GetTabCreationParams{UserID: hub.owner.String(), WorkerID: hub.workerID, TabID: "process-restart-file"})
	require.NoError(t, err)
	assert.Equal(t, creationCommitted, row.State)
	assert.Zero(t, len(row.RequestPayload))
	assert.Zero(t, len(row.RuntimePayload))
	payload, err := fresh.TabPayloads.Get(ctx, hub.owner.String(), row.TabID)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(directory, row.TabID+".txt"), payload.GetFile().GetFilePath())
	assert.Len(t, hub.manager.Materialized(crdt.SubscriberFilter{}).GetTabs(), 1)
	_, err = hub.store.WorkerTabPublications().Get(ctx, store.WorkerTabPublicationKey{UserID: hub.owner, WorkerID: hub.workerID, TabID: row.TabID})
	assert.NoError(t, err)
}
