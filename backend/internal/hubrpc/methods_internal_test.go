package hubrpc

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
)

func TestRegistryKeysEachDescriptorByItsProcedure(t *testing.T) {
	got := registry(
		entry[leapmuxv1.LocateTabRequest, leapmuxv1.LocateTabResponse](leapmuxv1connect.WorkspaceServiceLocateTabProcedure),
		entry[leapmuxv1.GetMaterializedRequest, leapmuxv1.GetMaterializedResponse](leapmuxv1connect.UserCRDTGetMaterializedProcedure),
	)
	require.Len(t, got, 2)
	assert.Equal(t, leapmuxv1connect.WorkspaceServiceLocateTabProcedure, got[leapmuxv1connect.WorkspaceServiceLocateTabProcedure].Procedure)
	assert.Equal(t, leapmuxv1connect.UserCRDTGetMaterializedProcedure, got[leapmuxv1connect.UserCRDTGetMaterializedProcedure].Procedure)
	assert.NotContains(t, got, "LocateTab", "a bare method name is not an address")
}

func TestRegistryIsEmptyWithNoDescriptor(t *testing.T) {
	assert.Empty(t, registry())
}

// WorkerManagementService and AdminWorkerService both declare ListWorkers. The
// procedure path includes the service, so the registry holds both, each under
// its own key, where a key by method name could hold only one.
func TestRegistryKeepsOneMethodNameOfTwoServices(t *testing.T) {
	got := registry(
		entry[leapmuxv1.ListWorkersRequest, leapmuxv1.ListWorkersResponse](leapmuxv1connect.WorkerManagementServiceListWorkersProcedure),
		entry[leapmuxv1.AdminWorkerServiceListWorkersRequest, leapmuxv1.AdminWorkerServiceListWorkersResponse](leapmuxv1connect.AdminWorkerServiceListWorkersProcedure),
	)
	require.Len(t, got, 2)
	assert.IsType(t, &leapmuxv1.ListWorkersRequest{}, got[leapmuxv1connect.WorkerManagementServiceListWorkersProcedure].NewRequest())
	assert.IsType(t, &leapmuxv1.AdminWorkerServiceListWorkersRequest{}, got[leapmuxv1connect.AdminWorkerServiceListWorkersProcedure].NewRequest())
}

func TestRegistryPanicsOnAProcedureGivenTwice(t *testing.T) {
	assert.PanicsWithValue(t,
		`hubrpc: two descriptors give the procedure /leapmux.v1.WorkspaceService/LocateTab`,
		func() {
			registry(
				entry[leapmuxv1.LocateTabRequest, leapmuxv1.LocateTabResponse](leapmuxv1connect.WorkspaceServiceLocateTabProcedure),
				entry[leapmuxv1.LocateTabRequest, leapmuxv1.LocateTabResponse](leapmuxv1connect.WorkspaceServiceLocateTabProcedure),
			)
		})
}
