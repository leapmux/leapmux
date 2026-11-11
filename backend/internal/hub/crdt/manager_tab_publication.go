package crdt

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/tabcreation"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/lexorank"
	"google.golang.org/protobuf/proto"
)

// runTask joins caller-driven work on the canonical state writer. Once admitted,
// the task must finish before its caller can release captured state.
func (m *Manager) runTask(ctx context.Context, run func()) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-m.startedChan:
	default:
		return errors.New("the CRDT manager has not started")
	}
	done := make(chan struct{})
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-m.done:
		return errors.New("the CRDT manager stopped")
	case m.taskCh <- managerTask{run: run, done: done}:
	}
	<-done
	return nil
}

// PublishTab commits a Worker's already-persisted resource and durable result.
// The caller authenticates workerID; this method never accepts another owner.
func (m *Manager) PublishTab(ctx context.Context, workerID string, request *leapmuxv1.TabPublication) (*leapmuxv1.PublishTabResponse, error) {
	if workerID == "" || m.owner.IsZero() {
		return nil, errors.New("tab publication requires an owner and Worker")
	}
	if err := tabcreation.Validate(request.GetCreation(), request.GetTabType()); err != nil {
		return nil, err
	}
	captured := proto.CloneOf(request)
	hash, err := tabcreation.Fingerprint(captured)
	if err != nil {
		return nil, err
	}
	var result *leapmuxv1.PublishTabResponse
	var operationErr error
	err = m.runTask(ctx, func() { result, operationErr = m.publishTab(ctx, workerID, captured, hash) })
	if err != nil {
		return nil, err
	}
	return result, operationErr
}

func (m *Manager) publishTab(ctx context.Context, workerID string, request *leapmuxv1.TabPublication, hash []byte) (*leapmuxv1.PublishTabResponse, error) {
	tabID := request.GetCreation().GetTabId()
	previous, err := m.journal.LookupTabPublication(ctx, m.owner.String(), workerID, tabID)
	if err == nil {
		if previous == nil || !bytes.Equal(hash, previous.RequestHash) {
			return nil, errors.New("the tab ID was already used by a different creation request")
		}
		return proto.CloneOf(previous.Response), nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	receipt := PublicationReceipt{WorkerID: workerID, TabID: tabID, RequestHash: hash}
	reject := func(reason leapmuxv1.BatchRejectionReason, message string) (*leapmuxv1.PublishTabResponse, error) {
		receipt.Response = &leapmuxv1.PublishTabResponse{Outcome: &leapmuxv1.PublishTabResponse_Rejected{Rejected: &leapmuxv1.RejectedTabPublication{Reason: reason, Message: message}}}
		if err := m.journal.CommitRejectedTabPublication(ctx, m.owner.String(), receipt); err != nil {
			return nil, err
		}
		return receipt.Response, nil
	}
	if _, exists := m.state.GetTabs()[tabID]; exists {
		return reject(leapmuxv1.BatchRejectionReason_BATCH_REJECTION_VALUE_DOMAIN, "A tab with this ID already exists.")
	}
	placement := request.GetCreation().GetPlacement()
	resolved, err := resolveCreationPlacement(m.state, placement)
	if err != nil {
		return reject(leapmuxv1.BatchRejectionReason_BATCH_REJECTION_VALUE_DOMAIN, err.Error())
	}
	tab := &leapmuxv1.TabIdent{TabType: request.GetTabType(), TabId: tabID}
	publication := &leapmuxv1.PublishedTab{Tab: tab, WorkspaceId: placement.GetWorkspaceId(), TileId: resolved.tileID, Position: resolved.position}
	receipt.Response = &leapmuxv1.PublishTabResponse{Outcome: &leapmuxv1.PublishTabResponse_Published{Published: publication}}
	ops := []*leapmuxv1.CrdtOp{
		{OpId: id.Generate(), Body: &leapmuxv1.CrdtOp_SetTabRegister{SetTabRegister: &leapmuxv1.SetTabRegisterOp{Tab: tab, Field: &leapmuxv1.SetTabRegisterOp_TileId{TileId: resolved.tileID}}}},
		{OpId: id.Generate(), Body: &leapmuxv1.CrdtOp_SetTabRegister{SetTabRegister: &leapmuxv1.SetTabRegisterOp{Tab: tab, Field: &leapmuxv1.SetTabRegisterOp_Position{Position: resolved.position}}}},
		{OpId: id.Generate(), Body: &leapmuxv1.CrdtOp_SetTabRegister{SetTabRegister: &leapmuxv1.SetTabRegisterOp{Tab: tab, Field: &leapmuxv1.SetTabRegisterOp_WorkerId{WorkerId: workerID}}}},
	}
	ops = append(resolved.positionUpdates, ops...)
	presentation := request.GetCreation().GetPresentation()
	if presentation != nil {
		if presentation.DisplayMode != nil {
			ops = append(ops, &leapmuxv1.CrdtOp{OpId: id.Generate(), Body: &leapmuxv1.CrdtOp_SetTabRegister{SetTabRegister: &leapmuxv1.SetTabRegisterOp{Tab: tab, Field: &leapmuxv1.SetTabRegisterOp_DisplayMode{DisplayMode: presentation.GetDisplayMode()}}}})
		}
		if presentation.FileViewMode != nil {
			ops = append(ops, &leapmuxv1.CrdtOp{OpId: id.Generate(), Body: &leapmuxv1.CrdtOp_SetTabRegister{SetTabRegister: &leapmuxv1.SetTabRegisterOp{Tab: tab, Field: &leapmuxv1.SetTabRegisterOp_FileViewMode{FileViewMode: presentation.GetFileViewMode()}}}})
		}
		if presentation.FileDiffBase != nil {
			ops = append(ops, &leapmuxv1.CrdtOp{OpId: id.Generate(), Body: &leapmuxv1.CrdtOp_SetTabRegister{SetTabRegister: &leapmuxv1.SetTabRegisterOp{Tab: tab, Field: &leapmuxv1.SetTabRegisterOp_FileDiffBase{FileDiffBase: presentation.GetFileDiffBase()}}}})
		}
	}
	// Submit would enqueue back onto this goroutine. Run the normal validator and
	// commit directly, with user authorization and a single-Worker restriction.
	batch := &leapmuxv1.OpBatch{BatchId: id.Generate(), Epoch: m.state.GetCurrentEpoch(), Ops: ops}
	response := m.processSubmit(ctx, submitJob{input: SubmitInput{Batches: []*leapmuxv1.OpBatch{batch}, PrincipalID: m.owner.String(), OriginClient: m.hubClientID, WorkerScope: func(target string) bool { return target == workerID }, publication: &receipt}})
	if response.err != nil {
		return nil, response.err
	}
	if len(response.results) != 1 {
		return nil, errors.New("tab publication returned no batch result")
	}
	if rejected := response.results[0].GetRejected(); rejected != nil {
		return reject(rejected.GetReason(), "The Hub rejected the tab placement.")
	}
	if response.results[0].GetCommitted() == nil {
		return nil, errors.New("tab publication did not commit")
	}
	return receipt.Response, nil
}

type resolvedCreationPlacement struct {
	tileID          string
	position        string
	positionUpdates []*leapmuxv1.CrdtOp
}

func resolveCreationDestination(state *leapmuxv1.UserCrdtState, placement *leapmuxv1.TabPlacement) (string, error) {
	workspace := state.GetWorkspaces()[placement.GetWorkspaceId()]
	if workspace == nil {
		return "", errors.New("the destination workspace no longer exists")
	}
	tile := placement.GetTileId()
	if placement.GetFirstLeaf() != nil {
		children := BuildLiveChildrenIndex(state)
		visited := map[string]bool{}
		var firstLeaf func(string) string
		firstLeaf = func(nodeID string) string {
			if visited[nodeID] {
				return ""
			}
			visited[nodeID] = true
			node := state.GetNodes()[nodeID]
			if node == nil || !HLCIsZero(node.GetTombstoneAt()) {
				return ""
			}
			if node.GetKind().GetValue() == leapmuxv1.NodeKind_NODE_KIND_LEAF {
				return nodeID
			}
			ordered := slices.Clone(children[nodeID])
			slices.SortFunc(ordered, func(a, b string) int {
				if c := strings.Compare(state.GetNodes()[a].GetPosition().GetValue(), state.GetNodes()[b].GetPosition().GetValue()); c != 0 {
					return c
				}
				return strings.Compare(a, b)
			})
			for _, child := range ordered {
				if leaf := firstLeaf(child); leaf != "" {
					return leaf
				}
			}
			return ""
		}
		tile = firstLeaf(workspace.GetRootNodeId())
	}
	resolved, alive := resolveTileWorkspace(state, tile, registeredRoots(state))
	if tile == "" || !alive || resolved != placement.GetWorkspaceId() || !tileIsLeaf(state, tile) {
		return "", errors.New("the destination tile is no longer available in this workspace")
	}
	return tile, nil
}

func resolveCreationPlacement(state *leapmuxv1.UserCrdtState, placement *leapmuxv1.TabPlacement) (resolvedCreationPlacement, error) {
	result := resolvedCreationPlacement{}
	tile, err := resolveCreationDestination(state, placement)
	if err != nil {
		return result, err
	}
	result.tileID = tile
	// The destination is a live leaf on this workspace. Every live tab anchored
	// to it therefore survives projection; unrelated tiles need no projection.
	var siblings []*leapmuxv1.TabRecord
	for _, tab := range state.GetTabs() {
		if HLCIsZero(tab.GetTombstoneAt()) && tab.GetTileId().GetValue() == tile {
			siblings = append(siblings, tab)
		}
	}
	slices.SortFunc(siblings, func(a, b *leapmuxv1.TabRecord) int {
		if c := strings.Compare(a.GetPosition().GetValue(), b.GetPosition().GetValue()); c != 0 {
			return c
		}
		return strings.Compare(a.GetTabId(), b.GetTabId())
	})
	index := 0
	switch placement.GetInsertion() {
	case leapmuxv1.TabInsertion_TAB_INSERTION_FIRST:
	case leapmuxv1.TabInsertion_TAB_INSERTION_LAST:
		index = len(siblings)
	case leapmuxv1.TabInsertion_TAB_INSERTION_BEFORE, leapmuxv1.TabInsertion_TAB_INSERTION_AFTER:
		index = -1
		for i, tab := range siblings {
			if tab.GetTabId() == placement.GetRelativeTabId() {
				index = i
				break
			}
		}
		if index < 0 {
			return result, errors.New("the reference tab is no longer on the destination tile")
		}
		if placement.GetInsertion() == leapmuxv1.TabInsertion_TAB_INSERTION_AFTER {
			index++
		}
	default:
		return result, fmt.Errorf("unsupported tab insertion %s", placement.GetInsertion())
	}
	lower, upper := "", ""
	if index > 0 {
		lower = siblings[index-1].GetPosition().GetValue()
	}
	if index < len(siblings) {
		upper = siblings[index].GetPosition().GetValue()
	}
	candidate := lexorank.Mid(lower, upper)
	if (index == 0 || candidate > lower) && (index == len(siblings) || candidate < upper) && validCreationRank(candidate) {
		result.position = candidate
		return result, nil
	}
	// Ties and prefix-adjacent ranks may have no representable midpoint.
	// Normalize the complete visible order in the same batch as the new tab.
	ranks, err := spacedCreationRanks(len(siblings) + 1)
	if err != nil {
		return result, err
	}
	result.position = ranks[index]
	for i, tab := range siblings {
		rankIndex := i
		if i >= index {
			rankIndex++
		}
		rank := ranks[rankIndex]
		if rank == tab.GetPosition().GetValue() {
			continue
		}
		result.positionUpdates = append(result.positionUpdates, &leapmuxv1.CrdtOp{OpId: id.Generate(), Body: &leapmuxv1.CrdtOp_SetTabRegister{SetTabRegister: &leapmuxv1.SetTabRegisterOp{Tab: &leapmuxv1.TabIdent{TabType: tab.GetTabType(), TabId: tab.GetTabId()}, Field: &leapmuxv1.SetTabRegisterOp_Position{Position: rank}}}})
	}
	return result, nil
}

func validCreationRank(rank string) bool {
	if rank == "" {
		return false
	}
	for _, character := range []byte(rank) {
		if character < 'a' || character > 'z' {
			return false
		}
	}
	return true
}

// spacedCreationRanks uses fixed-width base-26 values with room between them.
// A trailing midpoint character leaves a representable predecessor for the
// first rank and avoids a minimum-character suffix after normalization.
func spacedCreationRanks(count int) ([]string, error) {
	if count <= 0 {
		return nil, errors.New("a positive tab count is required")
	}
	width, capacity := 1, uint64(26)
	denominator := uint64(count) + 1
	for capacity/denominator < 2 {
		if capacity > math.MaxUint64/26 {
			return nil, errors.New("too many tabs to normalize their positions")
		}
		capacity *= 26
		width++
	}
	step := capacity / denominator
	ranks := make([]string, count)
	for i := range ranks {
		value := uint64(i+1) * step
		rank := make([]byte, width+1)
		rank[width] = 'n'
		for digit := width - 1; digit >= 0; digit-- {
			rank[digit] = 'a' + byte(value%26)
			value /= 26
		}
		ranks[i] = string(rank)
	}
	return ranks, nil
}

// CancelTabPublication prevents a delayed creation from publishing after close.
// The receipt proves which creation owns any tab that this operation removes.
func (m *Manager) CancelTabPublication(ctx context.Context, workerID string, request *leapmuxv1.TabPublication) error {
	if workerID == "" || m.owner.IsZero() {
		return errors.New("tab cancellation requires an owner and Worker")
	}
	if err := tabcreation.Validate(request.GetCreation(), request.GetTabType()); err != nil {
		return err
	}
	captured := proto.CloneOf(request)
	hash, err := tabcreation.Fingerprint(captured)
	if err != nil {
		return err
	}
	var operationErr error
	err = m.runTask(ctx, func() { operationErr = m.cancelTabPublication(ctx, workerID, captured, hash) })
	if err != nil {
		return err
	}
	return operationErr
}

func (m *Manager) cancelTabPublication(ctx context.Context, workerID string, request *leapmuxv1.TabPublication, hash []byte) error {
	tabID := request.GetCreation().GetTabId()
	previous, err := m.journal.LookupTabPublication(ctx, m.owner.String(), workerID, tabID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	receipt := PublicationReceipt{WorkerID: workerID, TabID: tabID, RequestHash: hash, Response: &leapmuxv1.PublishTabResponse{Outcome: &leapmuxv1.PublishTabResponse_Rejected{Rejected: &leapmuxv1.RejectedTabPublication{Reason: leapmuxv1.BatchRejectionReason_BATCH_REJECTION_VALUE_DOMAIN, Message: "Tab creation was canceled."}}}}
	if err == nil {
		if previous == nil || !bytes.Equal(previous.RequestHash, hash) {
			return errors.New("the tab ID was already used by a different creation request")
		}
		if previous.Response.GetRejected() != nil {
			return nil
		}
		published := previous.Response.GetPublished()
		if published == nil || published.GetTab().GetTabId() != tabID || published.GetTab().GetTabType() != request.GetTabType() {
			return errors.New("the stored publication does not match its creation")
		}
		receipt.Replace = true
		tab := m.state.GetTabs()[tabID]
		if tab != nil && HLCIsZero(tab.GetTombstoneAt()) {
			if tab.GetWorkerId().GetValue() != workerID || tab.GetTabType() != request.GetTabType() {
				// This receipt still owns the old creation, but not the reassigned tab.
				return m.journal.CommitRejectedTabPublication(ctx, m.owner.String(), receipt)
			}
			batch := &leapmuxv1.OpBatch{BatchId: id.Generate(), Epoch: m.state.GetCurrentEpoch(), Ops: []*leapmuxv1.CrdtOp{{OpId: id.Generate(), Body: &leapmuxv1.CrdtOp_TombstoneTab{TombstoneTab: &leapmuxv1.TombstoneTabOp{Tab: proto.CloneOf(published.GetTab())}}}}}
			response := m.processSubmit(ctx, submitJob{input: SubmitInput{Internal: true, PrincipalID: m.owner.String(), OriginClient: m.hubClientID, Batches: []*leapmuxv1.OpBatch{batch}, publication: &receipt}})
			if response.err != nil {
				return response.err
			}
			if len(response.results) != 1 || response.results[0].GetCommitted() == nil {
				return errors.New("tab cancellation did not commit")
			}
			return nil
		}
	}
	return m.journal.CommitRejectedTabPublication(ctx, m.owner.String(), receipt)
}

// ResolveTabPublication separates creation proof from current canonical liveness.
func (m *Manager) ResolveTabPublication(ctx context.Context, workerID string, request *leapmuxv1.TabPublication) (*leapmuxv1.ResolveTabPublicationResponse, error) {
	if workerID == "" || m.owner.IsZero() {
		return nil, errors.New("tab resolution requires an owner and Worker")
	}
	if err := tabcreation.Validate(request.GetCreation(), request.GetTabType()); err != nil {
		return nil, err
	}
	captured := proto.CloneOf(request)
	hash, err := tabcreation.Fingerprint(captured)
	if err != nil {
		return nil, err
	}
	var response *leapmuxv1.ResolveTabPublicationResponse
	var operationErr error
	err = m.runTask(ctx, func() {
		receipt, err := m.journal.LookupTabPublication(ctx, m.owner.String(), workerID, captured.GetCreation().GetTabId())
		if err != nil {
			operationErr = err
			return
		}
		if receipt == nil || !bytes.Equal(receipt.RequestHash, hash) {
			operationErr = errors.New("the tab ID belongs to a different creation request")
			return
		}
		response = &leapmuxv1.ResolveTabPublicationResponse{Receipt: proto.CloneOf(receipt.Response), Presence: leapmuxv1.TabPublicationPresence_TAB_PUBLICATION_PRESENCE_ABSENT}
		if receipt.Response.GetPublished() == nil {
			return
		}
		tab := m.state.GetTabs()[captured.GetCreation().GetTabId()]
		if tab == nil || !HLCIsZero(tab.GetTombstoneAt()) {
			return
		}
		if tab.GetWorkerId().GetValue() != workerID || tab.GetTabType() != captured.GetTabType() {
			response.Presence = leapmuxv1.TabPublicationPresence_TAB_PUBLICATION_PRESENCE_REASSIGNED
			return
		}
		if _, alive := resolveTileWorkspace(m.state, tab.GetTileId().GetValue(), registeredRoots(m.state)); !alive {
			return
		}
		hosted, err := m.journal.LookupHostedTabState(ctx, m.owner.String(), workerID, tab.GetTabId())
		if errors.Is(err, ErrNotFound) {
			return
		}
		if err != nil {
			operationErr = err
			return
		}
		if hosted.GetTab().GetTabType() != tab.GetTabType() || hosted.GetTab().GetTabId() != tab.GetTabId() {
			operationErr = errors.New("the hosted tab index disagrees with canonical state")
			return
		}
		switch hosted.GetArchiveState() {
		case leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ACTIVE, leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ARCHIVED:
		default:
			operationErr = errors.New("the hosted tab has no valid archive state")
			return
		}
		response.Presence = leapmuxv1.TabPublicationPresence_TAB_PUBLICATION_PRESENCE_HOSTED
		response.ArchiveState = hosted.GetArchiveState()
	})
	if err != nil {
		return nil, err
	}
	return response, operationErr
}
