package service

import (
	"context"
	"encoding/json"
	"errors"

	"connectrpc.com/connect"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/settings"
	"github.com/leapmux/leapmux/internal/hub/store"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// AdminSettingsService implements the leapmux.v1.AdminSettingsService
// ConnectRPC handler: the authenticated, online face of the hub-settings
// registry (the offline break-glass path is `leapmux recover`, which
// never goes through here).
type AdminSettingsService struct {
	set *settings.Manager
	// cfg supplies the deployment shape this handler filters on: solo mode
	// takes a key out of the whole administration surface (see hidden). The
	// per-key read-time rules are NOT here -- they belong to their keys, on
	// the manager (settings.WithEffective).
	cfg *config.Config
	// store carries the elevation slide alone. Every write here is a
	// sensitive action, and the hub's standing rule is that a sensitive
	// action slides the window that admitted it; a gated verb that does not
	// slide is a verb the window does not count as use. The settings rows
	// themselves are the manager's, not this service's.
	store store.Store

	// The clock this service reads. It compares the same elevation window
	// AdminUserService and UserService compare, so the three must answer one
	// instant or a test that moves the clock moves part of the surface.
	clockSeam
}

func NewAdminSettingsService(set *settings.Manager, cfg *config.Config, st store.Store) *AdminSettingsService {
	return &AdminSettingsService{set: set, cfg: cfg, store: st}
}

// settingValueToProto assembles one hub setting's wire value from the
// snapshot. THREE documents, and they are not interchangeable:
//
//   - value_json is the STORED row, verbatim and empty when no row exists.
//     It is what "the operator changed this" looks like, and the client
//     reads it per field to decide which fields to offer a reset for.
//   - merged_json is that row merged onto the code default, redacted.
//     Always present.
//   - effective_json is what the hub actually uses right now, which
//     differs from merged_json whenever a read-time rule overrides the
//     stored state.
//
// Sending the merged document as value_json made every field of every key
// read as customized on a virgin hub, because Decode merges the default in
// and the defaults are non-zero.
//
// The read-time rules live on the KEYS, registered at the hub's wiring site
// (settings.WithEffective). This handler asks the manager which value is in
// effect; it holds no per-key knowledge of its own.
func (s *AdminSettingsService) settingValueToProto(snap *settings.Snapshot, desc settings.Descriptor) *leapmuxv1.SettingValue {
	current := snap.ValueOf(desc)
	v := &leapmuxv1.SettingValue{
		Key:           desc.Name(),
		ValueJson:     string(snap.StoredValue(desc)),
		MergedJson:    marshalSettingJSON(desc.Redacted(current)),
		EffectiveJson: marshalSettingJSON(desc.Redacted(s.set.Effective(snap, desc))),
		Customized:    snap.Customized(desc),
	}
	if t := snap.UpdatedAt(desc); !t.IsZero() {
		v.UpdatedAt = timestamppb.New(t)
	}
	if desc.HasSecret() {
		v.SecretSet = secretSetOf(desc, current)
	}
	return v
}

// secretSetOf reports which secret fields carry a stored value, by
// splitting the merged value the same way the write path does: a field
// present in the secret half is stored; omitempty dropped it otherwise.
func secretSetOf(desc settings.Descriptor, v any) map[string]bool {
	out := map[string]bool{}
	_, secret, err := desc.Split(v)
	if err != nil || len(secret) == 0 {
		return out
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(secret, &doc); err != nil {
		return out
	}
	for _, f := range desc.SecretFieldNames() {
		if _, ok := doc[f]; ok {
			out[f] = true
		}
	}
	return out
}

func (s *AdminSettingsService) ListSettings(ctx context.Context, _ *connect.Request[leapmuxv1.ListSettingsRequest]) (*connect.Response[leapmuxv1.ListSettingsResponse], error) {
	snap := s.set.Snapshot(ctx)
	descrs := make([]*leapmuxv1.SettingDescriptor, 0)
	values := make([]*leapmuxv1.SettingValue, 0)
	for _, desc := range s.set.Registered() {
		if s.hidden(desc) {
			continue
		}
		descrs = append(descrs, settingDescriptorToProto(desc))
		values = append(values, s.settingValueToProto(snap, desc))
	}
	return connect.NewResponse(&leapmuxv1.ListSettingsResponse{
		Descriptors: descrs,
		Values:      values,
	}), nil
}

// descriptorFor resolves one settings key for a write path, refusing an
// empty or unregistered key. Shared by the three write handlers so the
// argument validation and its wording cannot drift between them.
func (s *AdminSettingsService) descriptorFor(key string) (settings.Descriptor, error) {
	if key == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("key is required"))
	}
	desc, ok := s.set.Descriptor(key)
	if !ok {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("unknown setting key "+key))
	}
	// The SAME test ListSettings applies. HiddenInSolo is documented as
	// taking the key out of the whole administration surface, but only the
	// listing enforced it, so a key an operator could not read was one the
	// operator could still write -- and eleven keys are hidden in solo.
	if s.hidden(desc) {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			errors.New("setting key "+key+" is not administrable in solo mode"))
	}
	return desc, nil
}

// hidden reports whether this deployment omits the key from the whole
// administration surface. One predicate, so the read filter and the write
// filter cannot disagree.
func (s *AdminSettingsService) hidden(desc settings.Descriptor) bool {
	return desc.UI().HiddenInSolo && s.cfg.SoloMode
}

// writeUnderElevation admits a write to the hub's configuration, runs it, and
// records that the window was used.
//
// EVERY write handler takes it, and READS take none. A hub setting is
// deployment-wide, and several of these keys are the hub's own security
// controls: sign-up, captcha, the rate limits, SMTP, and the public_url the
// passkey relying party derives from. A stolen administrator cookie that
// could turn those off buys more than any single account mutation the
// elevation window already guards -- so the window guards these too, and
// the surface no longer asks a user to verify before renaming themselves
// while it lets the same session open the hub to the world.
//
// requireElevatedActor, not requireElevation: an admin-scoped bearer holds
// no session row to stamp and can never elevate, and
// `leapmux control admin settings ...` is the documented headless path.
// That scope is granted only at a browser consent that itself required an
// elevated session, so the factor was proven once for the credential.
// Refusing it here would break the CLI outright rather than ask it for
// anything.
//
// It also SLIDES the window the write used, which is why every handler goes
// through this rather than calling the gate itself: the hub's standing rule
// is that a sensitive action slides the window that admitted it, and a gated
// verb that forgot the slide would be a verb the window does not count as
// use. One helper means the next write verb cannot get half of it.
//
// It runs AFTER each handler's argument validation, which is where the hub
// puts an argument check that belongs to the caller's own typing (see
// RequestEmailChange). An unknown key is the caller's mistake, and reporting
// it first spares a verification prompt answered for nothing.
func (s *AdminSettingsService) writeUnderElevation(ctx context.Context, write func() error) error {
	actor, err := requireElevatedActor(ctx, s.now())
	if err != nil {
		return err
	}
	if err := write(); err != nil {
		return err
	}
	slideElevation(ctx, s.store, actor, s.now())
	return nil
}

// UpdateSetting merges a partial document onto one key's public half.
// UpdateSettingSecret does NOT route through it: the two write different
// halves of the row, so they call different Manager verbs. What they DO
// share is descriptorFor, which is where an argument check belongs if it
// must cover both.
func (s *AdminSettingsService) UpdateSetting(ctx context.Context, req *connect.Request[leapmuxv1.UpdateSettingRequest]) (*connect.Response[leapmuxv1.UpdateSettingResponse], error) {
	desc, err := s.descriptorFor(req.Msg.GetKey())
	if err != nil {
		return nil, err
	}
	if err := s.writeUnderElevation(ctx, func() error {
		if err := s.set.Update(ctx, desc, json.RawMessage(req.Msg.GetPartialJson())); err != nil {
			return settingWriteConnectError(err)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return connect.NewResponse(&leapmuxv1.UpdateSettingResponse{
		Value: s.settingValueToProto(s.set.Snapshot(ctx), desc),
		// The write is stored, but a restart-class key keeps its previous
		// value in this process until it restarts. Report that here rather
		// than leave the caller to look the descriptor up again.
		Restart: desc.Propagation() == settings.PropagationRestart,
	}), nil
}

func (s *AdminSettingsService) UpdateSettingSecret(ctx context.Context, req *connect.Request[leapmuxv1.UpdateSettingSecretRequest]) (*connect.Response[leapmuxv1.UpdateSettingSecretResponse], error) {
	desc, err := s.descriptorFor(req.Msg.GetKey())
	if err != nil {
		return nil, err
	}
	if err := s.writeUnderElevation(ctx, func() error {
		if err := s.set.UpdateSecret(ctx, desc, json.RawMessage(req.Msg.GetPartialJson())); err != nil {
			return settingWriteConnectError(err)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return connect.NewResponse(&leapmuxv1.UpdateSettingSecretResponse{
		Value: s.settingValueToProto(s.set.Snapshot(ctx), desc),
	}), nil
}

// UpdateSettings applies several keys' edits atomically.
func (s *AdminSettingsService) UpdateSettings(ctx context.Context, req *connect.Request[leapmuxv1.UpdateSettingsRequest]) (*connect.Response[leapmuxv1.UpdateSettingsResponse], error) {
	msgs := req.Msg.GetWrites()
	if len(msgs) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("writes is required"))
	}
	writes := make([]settings.KeyWrite, 0, len(msgs))
	for _, w := range msgs {
		desc, err := s.descriptorFor(w.GetKey())
		if err != nil {
			return nil, err
		}
		// A write that carries neither half, an inert document, and a secret
		// half for a key with no secret fields are all Manager.UpdateMany's
		// refusals now. Restating one of them here gave the same rule two
		// messages, and the handler is not the layer that knows which fields
		// a key declares.
		writes = append(writes, settings.KeyWrite{
			Desc:   desc,
			Public: json.RawMessage(w.GetPartialJson()),
			Secret: json.RawMessage(w.GetSecretPartialJson()),
		})
	}
	if err := s.writeUnderElevation(ctx, func() error {
		if err := s.set.UpdateMany(ctx, writes); err != nil {
			return settingWriteConnectError(err)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	// One snapshot for the whole reply: every value the caller listed comes
	// from the same post-write view, so two keys of one transaction cannot
	// be reported from different moments.
	snap := s.set.Snapshot(ctx)
	values := make([]*leapmuxv1.SettingValue, 0, len(writes))
	for _, w := range writes {
		values = append(values, s.settingValueToProto(snap, w.Desc))
	}
	return connect.NewResponse(&leapmuxv1.UpdateSettingsResponse{Values: values}), nil
}

func (s *AdminSettingsService) ResetSetting(ctx context.Context, req *connect.Request[leapmuxv1.ResetSettingRequest]) (*connect.Response[leapmuxv1.ResetSettingResponse], error) {
	desc, err := s.descriptorFor(req.Msg.GetKey())
	if err != nil {
		return nil, err
	}
	// The post-reset step is PART of the write: a reset is not complete until
	// it ran, so a failure there must not count as use of the window either.
	if err := s.writeUnderElevation(ctx, func() error {
		if err := s.set.Reset(ctx, desc); err != nil {
			return settingWriteConnectError(err)
		}
		return s.runAfterReset(ctx, []settings.Descriptor{desc})
	}); err != nil {
		return nil, err
	}
	return connect.NewResponse(&leapmuxv1.ResetSettingResponse{
		Value: s.settingValueToProto(s.set.Snapshot(ctx), desc),
	}), nil
}

// ResetSettings clears several keys in ONE transaction.
func (s *AdminSettingsService) ResetSettings(ctx context.Context, req *connect.Request[leapmuxv1.ResetSettingsRequest]) (*connect.Response[leapmuxv1.ResetSettingsResponse], error) {
	keys := req.Msg.GetKeys()
	if len(keys) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("keys is required"))
	}
	descs := make([]settings.Descriptor, 0, len(keys))
	for _, key := range keys {
		desc, err := s.descriptorFor(key)
		if err != nil {
			return nil, err
		}
		descs = append(descs, desc)
	}
	if err := s.writeUnderElevation(ctx, func() error {
		if err := s.set.ResetMany(ctx, descs); err != nil {
			return settingWriteConnectError(err)
		}
		return s.runAfterReset(ctx, descs)
	}); err != nil {
		return nil, err
	}
	// One snapshot for the whole reply, so two keys of one transaction
	// cannot be reported from different moments.
	snap := s.set.Snapshot(ctx)
	values := make([]*leapmuxv1.SettingValue, 0, len(descs))
	for _, desc := range descs {
		values = append(values, s.settingValueToProto(snap, desc))
	}
	return connect.NewResponse(&leapmuxv1.ResetSettingsResponse{Values: values}), nil
}

// runAfterReset runs each reset key's post-reset step. Both reset handlers
// share it, so the rule holds whichever verb the caller used.
//
// The steps belong to the KEYS, registered at the hub's wiring site
// (settings.WithAfterReset). The ALTCHA signing key is the one that has a
// step today: a reset removes the secret every outstanding challenge carries,
// and the hub's standing rule is that the request path never writes settings,
// so the reset re-provisions before it answers rather than leaving the next
// unauthenticated login to write hub_settings from its own handler.
//
// It runs AFTER the reset commits, which is why the manager does not fire it
// from inside ResetMany: the step writes settings itself.
func (s *AdminSettingsService) runAfterReset(ctx context.Context, descs []settings.Descriptor) error {
	for _, desc := range descs {
		if err := s.set.AfterReset(ctx, desc); err != nil {
			return connect.NewError(connect.CodeInternal, err)
		}
	}
	return nil
}

// settingWriteConnectError maps the manager's real error modes onto
// Connect codes, because the admin surface surfaces them verbatim:
// a validation or unknown-field refusal is the caller's to fix
// (InvalidArgument); a secret the keystore cannot decrypt must stop the
// write rather than destroy it (FailedPrecondition, message passed
// through — it contains the reencrypt instructions); anything else is a
// store fault (Internal).
//
// The classification reads TYPES, never message text. Matching on a
// substring made every one of these error strings load-bearing: rewording
// one silently downgraded an actionable InvalidArgument to a 500, with no
// compiler or test signal at the edit site.
func settingWriteConnectError(err error) error {
	var undecryptable *settings.SecretUndecryptableError
	if errors.As(err, &undecryptable) {
		return connect.NewError(connect.CodeFailedPrecondition, err)
	}
	var invalid *settings.InvalidError
	if errors.As(err, &invalid) {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewError(connect.CodeInternal, err)
}
