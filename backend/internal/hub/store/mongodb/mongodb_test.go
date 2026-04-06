//go:build integration

package mongodb_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/store"
	mongostore "github.com/leapmux/leapmux/internal/hub/store/mongodb"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
	"github.com/leapmux/leapmux/internal/util/testutil"
)

func TestMongoDBStore(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	testutil.ConfigureDockerHost(t)

	ctx := context.Background()

	// Start a MongoDB container with replica set for transaction support.
	req := testcontainers.ContainerRequest{
		Image:        "mongo:7",
		ExposedPorts: []string{"27017/tcp"},
		Cmd:          []string{"mongod", "--replSet", "rs0"},
		WaitingFor:   wait.ForListeningPort("27017/tcp"),
	}
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = container.Terminate(ctx) })

	// Initialize the replica set and wait for primary election.
	exitCode, _, err := container.Exec(ctx, []string{
		"mongosh", "--eval", `
			rs.initiate({_id: 'rs0', members: [{_id: 0, host: 'localhost:27017'}]});
			while (!rs.isMaster().ismaster) { sleep(100); }
		`,
	})
	require.NoError(t, err)
	require.Equal(t, 0, exitCode)

	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "27017")
	require.NoError(t, err)

	uri := fmt.Sprintf("mongodb://%s:%s/?directConnection=true", host, port.Port())

	st, err := mongostore.NewTestable(ctx, config.MongoDBConfig{
		URI:      uri,
		Database: "leapmux_test",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	suite := &storetest.Suite{
		NewStore: func(t *testing.T) store.TestableStore {
			t.Helper()
			err = st.TestHelper().TruncateAll(context.Background())
			require.NoError(t, err)
			return st
		},
	}
	suite.Run(t)
}
