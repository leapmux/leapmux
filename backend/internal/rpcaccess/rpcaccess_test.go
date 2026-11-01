package rpcaccess

import (
	"path"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/authscope"
)

func TestEveryServiceDeclaresAHost(t *testing.T) {
	all := All()
	require.NotEmpty(t, all, "the index found no leapmux.v1 method -- the descriptor walk is broken")
	for _, m := range all {
		assert.NotEqualf(t, leapmuxv1.ServiceHost_SERVICE_HOST_UNSPECIFIED, m.Host,
			"%s belongs to a service with no (leapmux.v1.host) option", m.Procedure)
	}
}

func TestEveryHubMethodStatesItsAccess(t *testing.T) {
	hub := Methods(leapmuxv1.ServiceHost_SERVICE_HOST_HUB)
	require.NotEmpty(t, hub)
	for _, m := range hub {
		if !assert.NotNilf(t, m.Access, "hub method %s declares no (leapmux.v1.access) option", m.Procedure) {
			continue
		}
		switch req := m.Access.GetRequirement().(type) {
		case *leapmuxv1.MethodAccess_Public:
		case *leapmuxv1.MethodAccess_Scope:
			scope := req.Scope
			assert.Truef(t, scope == leapmuxv1.Scope_SCOPE_NEVER || authscope.IsGrantable(scope),
				"hub method %s requires %s, which is neither SCOPE_NEVER nor grantable", m.Procedure, scope)
		default:
			assert.Failf(t, "no requirement", "hub method %s has a MethodAccess with no requirement selected", m.Procedure)
		}
	}
}

func TestEveryWorkerMethodRequiresAGrantableScope(t *testing.T) {
	worker := Methods(leapmuxv1.ServiceHost_SERVICE_HOST_WORKER)
	require.NotEmpty(t, worker)
	for _, m := range worker {
		req, ok := m.Access.GetRequirement().(*leapmuxv1.MethodAccess_Scope)
		if !assert.Truef(t, ok, "worker method %s must require one scope, got %v", m.Procedure, m.Access) {
			continue
		}
		assert.Truef(t, authscope.IsGrantable(req.Scope),
			"worker method %s requires %s, which no grant can carry", m.Procedure, req.Scope)
	}
}

func TestNoWorkerIPCMethodStatesAccess(t *testing.T) {
	ipc := Methods(leapmuxv1.ServiceHost_SERVICE_HOST_WORKER_IPC)
	require.NotEmpty(t, ipc)
	for _, m := range ipc {
		assert.Nilf(t, m.Access, "worker IPC method %s states access, which nothing enforces", m.Procedure)
	}
}

func TestWorkerMethodNamesAreUniqueAcrossServices(t *testing.T) {
	seen := map[string]string{}
	for _, m := range Methods(leapmuxv1.ServiceHost_SERVICE_HOST_WORKER) {
		name := path.Base(m.Procedure)
		if prior, dup := seen[name]; dup {
			assert.Failf(t, "duplicate worker method name", "%s and %s share the method name %s", prior, m.Procedure, name)
		}
		seen[name] = m.Procedure
	}
}

func TestLookupReportsTheDeclaredShape(t *testing.T) {
	open, ok := Lookup(leapmuxv1connect.WorkerTunnelServiceOpenTunnelConnProcedure)
	require.True(t, ok)
	assert.True(t, open.ServerStreaming)
	assert.Equal(t, leapmuxv1.ServiceHost_SERVICE_HOST_WORKER, open.Host)
	assert.Equal(t, leapmuxv1.Scope_SCOPE_TUNNEL_OPEN, open.Access.GetScope())

	send, ok := Lookup(leapmuxv1connect.WorkerTunnelServiceSendTunnelDataProcedure)
	require.True(t, ok)
	assert.False(t, send.ServerStreaming)

	login, ok := Lookup(leapmuxv1connect.AuthServiceLoginProcedure)
	require.True(t, ok)
	assert.Equal(t, leapmuxv1.ServiceHost_SERVICE_HOST_HUB, login.Host)
	assert.NotNil(t, login.Access.GetPublic())
}

func TestLookupMissesAnUndeclaredProcedure(t *testing.T) {
	for _, procedure := range []string{"", "OpenAgent", "/leapmux.v1.WorkerAgentService/NoSuchMethod", "/other.v1.Service/Method"} {
		_, ok := Lookup(procedure)
		assert.Falsef(t, ok, "Lookup(%q)", procedure)
	}
}

func TestLookupReturnsAnIndependentCopy(t *testing.T) {
	first, ok := Lookup(leapmuxv1connect.WorkerAgentServiceOpenAgentProcedure)
	require.True(t, ok)
	first.Access.Requirement = &leapmuxv1.MethodAccess_Scope{Scope: leapmuxv1.Scope_SCOPE_ALL}

	second, ok := Lookup(leapmuxv1connect.WorkerAgentServiceOpenAgentProcedure)
	require.True(t, ok)
	assert.Equal(t, leapmuxv1.Scope_SCOPE_AGENT_WRITE, second.Access.GetScope())
}

// Methods and All hand out copies too, so a caller that changes a result can
// change neither the index nor what the next call returns.
func TestMethodsAndAllReturnIndependentCopies(t *testing.T) {
	const procedure = leapmuxv1connect.WorkerAgentServiceOpenAgentProcedure
	never := &leapmuxv1.MethodAccess_Scope{Scope: leapmuxv1.Scope_SCOPE_NEVER}

	for _, m := range Methods(leapmuxv1.ServiceHost_SERVICE_HOST_WORKER) {
		if m.Procedure == procedure {
			m.Access.Requirement = never
		}
	}
	for _, m := range All() {
		if m.Procedure == procedure {
			m.Access.Requirement = never
		}
	}

	again, ok := Lookup(procedure)
	require.True(t, ok)
	assert.Equal(t, leapmuxv1.Scope_SCOPE_AGENT_WRITE, again.Access.GetScope())
}

// A method that declares no access option keeps a nil Access through the copy,
// so a caller can tell "declares none" from "declares an empty option".
func TestLookupKeepsAnAbsentAccessOptionAbsent(t *testing.T) {
	whoami, ok := Lookup(leapmuxv1connect.ControlIPCServiceWhoamiProcedure)
	require.True(t, ok)
	assert.Equal(t, leapmuxv1.ServiceHost_SERVICE_HOST_WORKER_IPC, whoami.Host)
	assert.Nil(t, whoami.Access)
}

func TestAllIsSortedAndCoversEveryHost(t *testing.T) {
	all := All()
	assert.True(t, sort.SliceIsSorted(all, func(i, j int) bool { return all[i].Procedure < all[j].Procedure }),
		"All is not sorted")
	total := 0
	for _, host := range []leapmuxv1.ServiceHost{
		leapmuxv1.ServiceHost_SERVICE_HOST_HUB,
		leapmuxv1.ServiceHost_SERVICE_HOST_WORKER,
		leapmuxv1.ServiceHost_SERVICE_HOST_WORKER_IPC,
	} {
		total += len(Methods(host))
	}
	assert.Equal(t, len(all), total, "every method belongs to exactly one of the three hosts")
}

func TestMethodsAreSortedAndFilteredByHost(t *testing.T) {
	for _, host := range []leapmuxv1.ServiceHost{
		leapmuxv1.ServiceHost_SERVICE_HOST_HUB,
		leapmuxv1.ServiceHost_SERVICE_HOST_WORKER,
		leapmuxv1.ServiceHost_SERVICE_HOST_WORKER_IPC,
	} {
		methods := Methods(host)
		assert.Truef(t, sort.SliceIsSorted(methods, func(i, j int) bool { return methods[i].Procedure < methods[j].Procedure }),
			"Methods(%s) is not sorted", host)
		for _, m := range methods {
			assert.Equal(t, host, m.Host)
		}
	}
	assert.Empty(t, Methods(leapmuxv1.ServiceHost_SERVICE_HOST_UNSPECIFIED))
	assert.Len(t, All(), len(index))
}

func TestConstName(t *testing.T) {
	assert.Equal(t, "WorkerAgentServiceOpenAgentProcedure", ConstName(leapmuxv1connect.WorkerAgentServiceOpenAgentProcedure))
	assert.Equal(t, "AuthServiceLoginProcedure", ConstName(leapmuxv1connect.AuthServiceLoginProcedure))
	for _, bad := range []string{"", "OpenAgent", "/leapmux.v1.Service", "/leapmux.v1.Service/", "/leapmux.v1./Method", "/other.v1.Service/Method", "/leapmux.v1.A/B/C"} {
		assert.Emptyf(t, ConstName(bad), "ConstName(%q)", bad)
	}
}
