package cmd

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli/remote"
	"github.com/leapmux/leapmux/internal/cli/remote/resolve"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/leapmux/leapmux/internal/util/id"
)

// cliOriginClientID is set once per CLI invocation. The hub overwrites
// origin_client_id on commit, so this is just an HLC-tiebreaker hint
// that keeps locally-generated ops distinguishable in stream echoes.
var (
	cliOriginClientIDOnce sync.Once
	cliOriginClientID     string
)

// originClientID returns the per-process CLI origin id, lazily seeded
// from a fresh nanoid the first time it's read.
func originClientID() string {
	cliOriginClientIDOnce.Do(func() {
		cliOriginClientID = id.Generate()
	})
	return cliOriginClientID
}

// CRDTBootstrap is the snapshot a one-shot WatchOrg subscribe returns.
// Holds the canonical state needed to build subsequent op batches
// (epoch + max_hlc seed for the local clock).
type CRDTBootstrap struct {
	OrgID        string
	State        *leapmuxv1.OrgMaterialized
	Epoch        int64
	Clock        *crdt.Clock
	OriginClient string
}

// crdtBootstrap opens a one-shot `/ws/orgevents` subscription, reads
// the initial `OrgMaterialized` event, and tears the connection down.
// Works for both hub-bound clients (`--hub <url>` opens the WS
// directly via the bearer token) and worker-spawned local-IPC
// clients (the worker's `RemoteIPCService.StreamInner` route opens
// the upstream WS using the agent's per-(user, workspace) delegation
// bearer; see `internal/worker/remoteipc/hub_stream.go`).
func crdtBootstrap(ctx context.Context, c *remote.Client, orgID string, workspaceIDs []string) (*CRDTBootstrap, error) {
	if orgID == "" {
		return nil, errors.New("crdt bootstrap: org_id required")
	}
	var initial *leapmuxv1.OrgMaterialized
	var err error
	if c.IsLocal() {
		initial, err = crdtBootstrapLocal(ctx, c, orgID, workspaceIDs)
	} else {
		initial, err = crdtBootstrapHub(ctx, c, orgID, workspaceIDs)
	}
	if err != nil {
		return nil, err
	}
	if initial == nil {
		return nil, errors.New("crdt bootstrap: stream closed before initial materialized event")
	}
	clock := crdt.NewClock(originClientID())
	clock.Observe(initial.GetMaxHlc())
	return &CRDTBootstrap{
		OrgID:        orgID,
		State:        initial,
		Epoch:        initial.GetCurrentEpoch(),
		Clock:        clock,
		OriginClient: originClientID(),
	}, nil
}

func crdtBootstrapHub(ctx context.Context, c *remote.Client, orgID string, workspaceIDs []string) (*leapmuxv1.OrgMaterialized, error) {
	// One-shot snapshot via the unary GetMaterialized RPC. Avoids the
	// WS handshake + first-event-await dance the streaming
	// `/ws/orgevents` path requires per CLI invocation.
	req := &leapmuxv1.GetMaterializedRequest{OrgId: orgID, WorkspaceIds: workspaceIDs}
	var resp leapmuxv1.GetMaterializedResponse
	if err := hubCallUnary(ctx, c, "GetMaterialized", "", req, &resp); err != nil {
		return nil, err
	}
	return resp.GetState(), nil
}

func crdtBootstrapLocal(ctx context.Context, c *remote.Client, orgID string, workspaceIDs []string) (*leapmuxv1.OrgMaterialized, error) {
	// Local-IPC path routes through the worker's RemoteIPC tunnel,
	// which proxies to the hub's unary GetMaterialized for the same
	// one-shot semantics as the remote path.
	req := &leapmuxv1.GetMaterializedRequest{OrgId: orgID, WorkspaceIds: workspaceIDs}
	payload, err := proto.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal getmaterialized: %w", err)
	}
	innerReq := &leapmuxv1.CallInnerRequest{
		Method:  "hub.GetMaterialized",
		Payload: payload,
	}
	if len(workspaceIDs) > 0 {
		innerReq.WorkspaceId = workspaceIDs[0]
	}
	innerResp, err := c.RemoteIPCService().CallInner(ctx, connect.NewRequest(innerReq))
	if err != nil {
		return nil, fmt.Errorf("crdt bootstrap (local IPC): %w", err)
	}
	if innerResp.Msg.GetIsError() {
		return nil, fmt.Errorf("crdt bootstrap (local IPC): %s", innerResp.Msg.GetErrorMessage())
	}
	var resp leapmuxv1.GetMaterializedResponse
	if err := proto.Unmarshal(innerResp.Msg.GetPayload(), &resp); err != nil {
		return nil, fmt.Errorf("decode getmaterialized response: %w", err)
	}
	return resp.GetState(), nil
}

// streamWatchOrgLocal opens a hub.WatchOrg StreamInner against the
// agent's local-IPC peer and drives `onEvent` for every decoded
// `WatchOrgEvent`. `onEvent` returns (stop=true, _) to terminate the
// loop after the current event (e.g. once the initial frame lands)
// or (_, err) to propagate an error. The stream is closed when
// streamWatchOrgLocal returns. Shared between the bootstrap reader
// (which stops on the initial frame) and `events --local` (which
// stays open until ctx cancellation).
func streamWatchOrgLocal(
	ctx context.Context,
	c *remote.Client,
	orgID string,
	workspaceIDs []string,
	onEvent func(*leapmuxv1.WatchOrgEvent) (stop bool, err error),
) error {
	req := &leapmuxv1.WatchOrgRequest{OrgId: orgID, WorkspaceIds: workspaceIDs}
	payload, err := proto.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal watchorg: %w", err)
	}
	streamReq := &leapmuxv1.StreamInnerRequest{
		Method:          "hub.WatchOrg",
		Payload:         payload,
		ClientRequestId: id.Generate(),
	}
	if len(workspaceIDs) > 0 {
		streamReq.WorkspaceId = workspaceIDs[0]
	}
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := c.RemoteIPCService().StreamInner(streamCtx, connect.NewRequest(streamReq))
	if err != nil {
		return err
	}
	defer func() { _ = stream.Close() }()
	for stream.Receive() {
		env := stream.Msg()
		if env.GetIsError() {
			return errors.New(env.GetErrorMessage())
		}
		if len(env.GetPayload()) == 0 {
			continue
		}
		var evt leapmuxv1.WatchOrgEvent
		if err := proto.Unmarshal(env.GetPayload(), &evt); err != nil {
			return fmt.Errorf("decode watchorg event: %w", err)
		}
		stop, err := onEvent(&evt)
		if err != nil {
			return err
		}
		if stop {
			return nil
		}
	}
	return stream.Err()
}

// crdtSubmitBatch submits a single OpBatch through the appropriate
// transport (hub-bound ConnectRPC or worker-spawned local IPC) and
// returns the per-batch result. The caller already holds the
// bootstrap-time epoch.
func crdtSubmitBatch(
	ctx context.Context,
	c *remote.Client,
	bs *CRDTBootstrap,
	workspaceID string,
	batch *leapmuxv1.OpBatch,
) (*leapmuxv1.BatchResult, error) {
	if bs == nil {
		return nil, errors.New("crdt submit: bootstrap is nil")
	}
	req := &leapmuxv1.SubmitOpsRequest{
		OrgId:   bs.OrgID,
		Epoch:   bs.Epoch,
		Batches: []*leapmuxv1.OpBatch{batch},
	}
	var resp leapmuxv1.SubmitOpsResponse
	if err := hubCallUnary(ctx, c, "SubmitOps", workspaceID, req, &resp); err != nil {
		return nil, err
	}
	if len(resp.GetResults()) != 1 {
		return nil, fmt.Errorf("submitops: expected 1 result, got %d", len(resp.GetResults()))
	}
	return resp.GetResults()[0], nil
}

// crdtCall bundles every value a CRDT-mutation subcommand needs after
// the standard preamble: a deadlined context, the resolved hub client,
// the workspace's bootstrap snapshot (epoch + initial state), and the
// workspace id used for transport routing.
//
// Callers must invoke `close()` to release the deadline cancel.
type crdtCall struct {
	ctx         context.Context
	cancel      context.CancelFunc
	c           *remote.Client
	bs          *CRDTBootstrap
	workspaceID string
}

// openCRDTCall runs the standard CRDT-mutation preamble: requireClient
// → rpcDeadline → resolveOrgID → crdtBootstrap. Errors are wrapped into
// the matching `remote.EmitError*` envelope so callers can `return err`
// directly without re-classifying.
func openCRDTCall(hub, workspaceID string) (*crdtCall, error) {
	c, err := requireClient(hub)
	if err != nil {
		return nil, err
	}
	ctx, cancel := rpcDeadline(context.Background())
	orgID, err := resolveOrgID(ctx, c, workspaceID)
	if err != nil {
		cancel()
		return nil, remote.EmitErrorWith("resolve_failed", err)
	}
	return finishOpenCRDTCall(ctx, cancel, c, orgID, workspaceID)
}

// openCRDTCallFromResolved skips the GetWorkspace round-trip when the
// caller already ran the universal resolver (`runResolve` →
// resolve.Resolved.OrgID). Handlers chain this after `runResolve(..,
// Need{WorkspaceID: true}, ..)` so the workspace lookup happens exactly
// once per command, regardless of how many openCRDTCall* call sites
// flow through.
func openCRDTCallFromResolved(hub string, got resolve.Resolved) (*crdtCall, error) {
	if got.WorkspaceID == "" {
		return nil, remote.EmitError("invalid_request", "workspace_id required")
	}
	if got.OrgID == "" {
		// The resolver returned a workspace without surfacing its
		// org; fall back to the unresolved path so we don't carry a
		// silent inconsistency forward.
		return openCRDTCall(hub, got.WorkspaceID)
	}
	c, err := requireClient(hub)
	if err != nil {
		return nil, err
	}
	ctx, cancel := rpcDeadline(context.Background())
	return finishOpenCRDTCall(ctx, cancel, c, got.OrgID, got.WorkspaceID)
}

func finishOpenCRDTCall(ctx context.Context, cancel context.CancelFunc, c *remote.Client, orgID, workspaceID string) (*crdtCall, error) {
	bs, err := crdtBootstrap(ctx, c, orgID, []string{workspaceID})
	if err != nil {
		cancel()
		return nil, remote.EmitErrorWith("crdt_bootstrap_failed", err)
	}
	return &crdtCall{ctx: ctx, cancel: cancel, c: c, bs: bs, workspaceID: workspaceID}, nil
}

// close releases the deadlined context. Safe on a nil receiver so
// callers can `defer cc.close()` immediately after openCRDTCall even
// when error handling rebinds `cc`.
func (cc *crdtCall) close() {
	if cc != nil && cc.cancel != nil {
		cc.cancel()
	}
}

// submitOps wraps ops in a fresh OpBatch, submits via the appropriate
// transport, and unwraps the per-batch rejection into an EmitError*
// envelope. Returns nil on commit.
func (cc *crdtCall) submitOps(ops []*leapmuxv1.OrgOp) error {
	res, err := crdtSubmitBatch(cc.ctx, cc.c, cc.bs, cc.workspaceID, crdtNewBatch(ops))
	if err != nil {
		return remote.EmitErrorWith("rpc_failed", err)
	}
	if err := crdtBatchError(res); err != nil {
		return remote.EmitErrorWith("batch_rejected", err)
	}
	return nil
}

// trySubmitOps is the non-emitting variant of submitOps. Returns
// (committed, rejection-reason, err). When committed=true, reason is
// UNSPECIFIED and err is nil. When committed=false, err is non-nil
// and reason carries the hub's BatchRejectionReason (or UNSPECIFIED
// for transport-level errors). Callers that want to branch on the
// rejection reason -- e.g. `layout set` retrying TAB_PLACEMENT_INVALID
// after re-bootstrapping the snapshot -- use this instead of
// submitOps so the envelope isn't emitted prematurely.
func (cc *crdtCall) trySubmitOps(ops []*leapmuxv1.OrgOp) (bool, leapmuxv1.BatchRejectionReason, error) {
	res, err := crdtSubmitBatch(cc.ctx, cc.c, cc.bs, cc.workspaceID, crdtNewBatch(ops))
	if err != nil {
		return false, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_UNSPECIFIED, err
	}
	if batchErr := crdtBatchError(res); batchErr != nil {
		reason := leapmuxv1.BatchRejectionReason_BATCH_REJECTION_UNSPECIFIED
		if rej := res.GetRejected(); rej != nil {
			reason = rej.GetReason()
		}
		return false, reason, batchErr
	}
	return true, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_UNSPECIFIED, nil
}

// crdtBatchError unwraps a BatchResult; returns nil for committed
// batches, an error keyed by rejection reason for rejected ones.
func crdtBatchError(res *leapmuxv1.BatchResult) error {
	if res == nil {
		return errors.New("nil batch result")
	}
	rej := res.GetRejected()
	if rej == nil {
		return nil
	}
	if offending := rej.GetOffendingOpId(); offending != "" {
		return fmt.Errorf("batch rejected: %s (offending op_id=%s)", rej.GetReason().String(), offending)
	}
	return fmt.Errorf("batch rejected: %s", rej.GetReason().String())
}

// crdtNewBatch wraps ops in a fresh OpBatch with a client-minted batch_id.
func crdtNewBatch(ops []*leapmuxv1.OrgOp) *leapmuxv1.OpBatch {
	return &leapmuxv1.OpBatch{BatchId: id.Generate(), Ops: ops}
}

// tombstoneCRDTTab is the convenience wrapper used by `agent close`,
// `terminal close`, and the `tab close` CLI: bootstraps a one-shot
// CRDT subscription, submits a single TombstoneTab op, and returns
// the per-batch error (or nil on commit). The caller has already
// done the worker-side close; we only need the CRDT half.
func tombstoneCRDTTab(ctx context.Context, c *remote.Client, workspaceID string, tabType leapmuxv1.TabType, tabID string) error {
	orgID, err := resolveOrgID(ctx, c, workspaceID)
	if err != nil {
		return fmt.Errorf("resolve org_id: %w", err)
	}
	bs, err := crdtBootstrap(ctx, c, orgID, []string{workspaceID})
	if err != nil {
		return fmt.Errorf("bootstrap: %w", err)
	}
	op := opTombstoneTab(bs, tabType, tabID)
	res, err := crdtSubmitBatch(ctx, c, bs, workspaceID, crdtNewBatch([]*leapmuxv1.OrgOp{op}))
	if err != nil {
		return fmt.Errorf("submit: %w", err)
	}
	return crdtBatchError(res)
}

// nowMillis is the wall-clock helper Clock.Tick takes.
func nowMillis() int64 {
	return time.Now().UnixMilli()
}

// op-builder helpers — each returns a single-register-write OrgOp
// stamped with a fresh advisory client_hlc from `bs.Clock`. The hub
// reassigns canonical HLCs on commit; client_hlc is just a hint.
//
// The Node-side counterparts live in layout.go. The proto generator
// makes the oneof `Field` interface package-private, so per-register
// helpers can't share a single "set field" parameter type. Instead
// they share the envelope-allocation core via newSetTabRegisterOp
// (returns both the wrapper OrgOp and the inner record), letting each
// helper drop to one line of variant-specific assignment.

func opTombstoneTab(bs *CRDTBootstrap, tabType leapmuxv1.TabType, tabID string) *leapmuxv1.OrgOp {
	op := envelope(bs)
	op.Body = &leapmuxv1.OrgOp_TombstoneTab{
		TombstoneTab: &leapmuxv1.TombstoneTabOp{TabType: tabType, TabId: tabID},
	}
	return op
}

func opTombstoneNode(bs *CRDTBootstrap, nodeID string) *leapmuxv1.OrgOp {
	op := envelope(bs)
	op.Body = &leapmuxv1.OrgOp_TombstoneNode{
		TombstoneNode: &leapmuxv1.TombstoneNodeOp{NodeId: nodeID},
	}
	return op
}

// newSetTabRegisterOp allocates the OrgOp wrapper + SetTabRegisterOp
// inner record and links them. Callers set inner.Field to the desired
// variant. Used by every opSetTab* helper below.
func newSetTabRegisterOp(bs *CRDTBootstrap, tabType leapmuxv1.TabType, tabID string) (*leapmuxv1.OrgOp, *leapmuxv1.SetTabRegisterOp) {
	op := envelope(bs)
	inner := &leapmuxv1.SetTabRegisterOp{TabType: tabType, TabId: tabID}
	op.Body = &leapmuxv1.OrgOp_SetTabRegister{SetTabRegister: inner}
	return op, inner
}

func opSetTabTileID(bs *CRDTBootstrap, tabType leapmuxv1.TabType, tabID, tileID string) *leapmuxv1.OrgOp {
	op, inner := newSetTabRegisterOp(bs, tabType, tabID)
	inner.Field = &leapmuxv1.SetTabRegisterOp_TileId{TileId: tileID}
	return op
}

func opSetTabPosition(bs *CRDTBootstrap, tabType leapmuxv1.TabType, tabID, position string) *leapmuxv1.OrgOp {
	op, inner := newSetTabRegisterOp(bs, tabType, tabID)
	inner.Field = &leapmuxv1.SetTabRegisterOp_Position{Position: position}
	return op
}

func opSetTabWorkerID(bs *CRDTBootstrap, tabType leapmuxv1.TabType, tabID, workerID string) *leapmuxv1.OrgOp {
	op, inner := newSetTabRegisterOp(bs, tabType, tabID)
	inner.Field = &leapmuxv1.SetTabRegisterOp_WorkerId{WorkerId: workerID}
	return op
}

func opSetTabDisplayMode(bs *CRDTBootstrap, tabType leapmuxv1.TabType, tabID string, mode int32) *leapmuxv1.OrgOp {
	op, inner := newSetTabRegisterOp(bs, tabType, tabID)
	inner.Field = &leapmuxv1.SetTabRegisterOp_DisplayMode{DisplayMode: mode}
	return op
}

func opSetTabFileViewMode(bs *CRDTBootstrap, tabType leapmuxv1.TabType, tabID string, mode int32) *leapmuxv1.OrgOp {
	op, inner := newSetTabRegisterOp(bs, tabType, tabID)
	inner.Field = &leapmuxv1.SetTabRegisterOp_FileViewMode{FileViewMode: mode}
	return op
}
