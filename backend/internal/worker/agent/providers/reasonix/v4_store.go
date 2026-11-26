package reasonix

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/klauspost/compress/zstd"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/util/pathutil"
)

const (
	reasonixV4SchemaVersion    = 4
	reasonixV4Codec            = "reasonix.session.linear/v4"
	reasonixV4FrameHeaderBytes = 12
	reasonixV4MaxFrameBytes    = 8 << 20
	reasonixV4SharedContentDir = "../.content-v1"
)

var reasonixV4FrameMagic = [4]byte{'R', 'X', '4', 'F'}

type reasonixV4Manifest struct {
	SchemaVersion int    `json:"schemaVersion"`
	Codec         string `json:"codec"`
	ContentRoot   string `json:"contentRoot"`
	SessionID     string `json:"sessionId"`
}

type reasonixV4ContentRef struct {
	Digest string `json:"digest"`
	Bytes  int64  `json:"bytes"`
}

type reasonixV4Record struct {
	SchemaVersion int              `json:"schemaVersion"`
	Codec         string           `json:"codec"`
	RecordType    string           `json:"recordType"`
	CommitID      string           `json:"commitId"`
	FirstSequence uint64           `json:"firstSeq"`
	EventCount    int              `json:"eventCount"`
	Event         *reasonixV4Event `json:"event"`
	SHA256        string           `json:"sha256"`
}

type reasonixV4Event struct {
	ID         string                `json:"id"`
	Sequence   uint64                `json:"seq"`
	Kind       string                `json:"kind"`
	Payload    []byte                `json:"payload"`
	PayloadRef *reasonixV4ContentRef `json:"payloadRef"`
}

type reasonixV3Commit struct {
	SchemaVersion int              `json:"schemaVersion"`
	RecordType    string           `json:"recordType"`
	Events        []reasonixV3Item `json:"events"`
}

type reasonixV3Item struct {
	Kind       string                `json:"kind"`
	Payload    json.RawMessage       `json:"payload"`
	PayloadRef *reasonixV4ContentRef `json:"payloadRef"`
}

// reasonixV4SessionDir locates a canonical sessions-v4/<sessionID> directory
// for sessionID. Reasonix maps each legacy sessions root to its sibling
// sessions-v4 root via RootForLegacyDir.
func reasonixV4SessionDir(q agent.StoredSessionQuery, sessionID, workingDir string) string {
	if !reasonixSessionIDIsSafe(sessionID) {
		return ""
	}
	var candidates []string
	if stateHome := strings.TrimSpace(q.Env("REASONIX_STATE_HOME")); stateHome != "" {
		candidates = append(candidates, filepath.Join(pathutil.ExpandHome(stateHome, q.Home()), "sessions-v4"))
	}
	for _, root := range reasonixSessionRoots(q, workingDir) {
		v4Root := filepath.Join(root, "sessions-v4")
		if filepath.Base(root) == "sessions" {
			v4Root = filepath.Join(filepath.Dir(root), "sessions-v4")
		}
		candidates = append(candidates, v4Root)
	}
	for _, v4Root := range candidates {
		dir := filepath.Join(v4Root, sessionID)
		if info, err := os.Stat(filepath.Join(dir, "manifest.json")); err == nil && info.Mode().IsRegular() {
			return dir
		}
	}
	return ""
}

func readReasonixV4SessionMessages(ctx context.Context, sessionDir, sessionID string) ([]reasonixJournalMessage, error) {
	manifestData, err := os.ReadFile(filepath.Join(sessionDir, "manifest.json"))
	if err != nil {
		return nil, err
	}
	var manifest reasonixV4Manifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return nil, fmt.Errorf("decode Reasonix session manifest: %w", err)
	}
	if manifest.SessionID != "" && manifest.SessionID != sessionID {
		return nil, fmt.Errorf("the Reasonix session manifest belongs to %q", manifest.SessionID)
	}
	contentRoot := reasonixV4ContentRoot(sessionDir, manifest.ContentRoot)

	if manifest.Codec == reasonixV4Codec {
		return readReasonixV4FramesMessages(ctx, filepath.Join(sessionDir, "events.frames"), contentRoot)
	}
	if manifest.SchemaVersion == 3 {
		return readReasonixV3EventsJSONL(ctx, filepath.Join(sessionDir, "events.jsonl"), contentRoot)
	}
	return nil, fmt.Errorf("unsupported Reasonix session codec %q (schema %d)", manifest.Codec, manifest.SchemaVersion)
}

func reasonixV4ContentRoot(sessionDir, configured string) string {
	root := filepath.Join(filepath.Dir(sessionDir), ".content-v1")
	trimmed := strings.TrimSpace(configured)
	if trimmed == "" {
		return root
	}
	candidate := filepath.Clean(filepath.Join(sessionDir, trimmed))
	relative, err := filepath.Rel(sessionDir, candidate)
	if err == nil && (relative == ".content-v1" || relative == reasonixV4SharedContentDir) {
		return candidate
	}
	return root
}

var reasonixV4DecoderPool sync.Pool

func acquireReasonixV4Decoder() (*zstd.Decoder, error) {
	if pooled := reasonixV4DecoderPool.Get(); pooled != nil {
		return pooled.(*zstd.Decoder), nil
	}
	return zstd.NewReader(nil,
		zstd.WithDecoderConcurrency(1),
		zstd.WithDecoderMaxMemory(reasonixV4MaxFrameBytes),
	)
}

func releaseReasonixV4Decoder(decoder *zstd.Decoder) {
	if decoder != nil {
		reasonixV4DecoderPool.Put(decoder)
	}
}

func isReasonixMessageEventKind(kind string) bool {
	switch kind {
	case "message/complete", "message/upsert", "message/retract", "history/replace", "legacy/import":
		return true
	default:
		return false
	}
}

func readReasonixV4FramesMessages(ctx context.Context, framesPath, contentRoot string) ([]reasonixJournalMessage, error) {
	file, _, err := openReasonixRegularFile(framesPath, "Reasonix v4 event log")
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()

	decoder, err := acquireReasonixV4Decoder()
	if err != nil {
		return nil, fmt.Errorf("create Reasonix v4 decoder: %w", err)
	}
	defer releaseReasonixV4Decoder(decoder)

	reader := bufio.NewReaderSize(file, 32<<10)
	var compressedBuf, rawBuf []byte

	type pendingBatch struct {
		commitID   string
		firstSeq   uint64
		eventCount int
		events     []reasonixV4Event
		digest     hash.Hash
	}
	var (
		pending *pendingBatch
		replay  reasonixMessageReplay
	)

	for {
		var raw []byte
		var complete bool
		var readErr error
		raw, compressedBuf, rawBuf, complete, readErr = readReasonixV4Frame(ctx, reader, decoder, compressedBuf, rawBuf)
		if readErr != nil {
			return nil, readErr
		}
		if !complete {
			return replay.messages, nil
		}
		var rec reasonixV4Record
		if err := json.Unmarshal(raw, &rec); err != nil {
			return nil, fmt.Errorf("decode Reasonix v4 record: %w", err)
		}
		if rec.SchemaVersion != reasonixV4SchemaVersion || rec.Codec != reasonixV4Codec {
			return nil, fmt.Errorf("unsupported Reasonix v4 record schema %d codec %q", rec.SchemaVersion, rec.Codec)
		}
		switch rec.RecordType {
		case "batch/begin":
			if pending != nil || rec.CommitID == "" || rec.EventCount <= 0 {
				return nil, fmt.Errorf("invalid Reasonix v4 batch begin")
			}
			d := sha256.New()
			_, _ = d.Write(raw)
			_, _ = d.Write([]byte{0})
			pending = &pendingBatch{
				commitID:   rec.CommitID,
				firstSeq:   rec.FirstSequence,
				eventCount: rec.EventCount,
				digest:     d,
			}
		case "batch/event":
			if pending == nil || rec.Event == nil || len(pending.events) >= pending.eventCount {
				return nil, fmt.Errorf("invalid Reasonix v4 batch event")
			}
			_, _ = pending.digest.Write(raw)
			_, _ = pending.digest.Write([]byte{0})
			pending.events = append(pending.events, *rec.Event)
		case "batch/end":
			if pending == nil || rec.CommitID != pending.commitID || rec.FirstSequence != pending.firstSeq ||
				rec.EventCount != pending.eventCount || len(pending.events) != pending.eventCount {
				return nil, fmt.Errorf("invalid Reasonix v4 batch end")
			}
			if got := hex.EncodeToString(pending.digest.Sum(nil)); rec.SHA256 != got {
				return nil, fmt.Errorf("the Reasonix v4 batch %s checksum mismatched", pending.commitID)
			}
			for _, ev := range pending.events {
				if !isReasonixMessageEventKind(ev.Kind) {
					continue
				}
				payload, resolveErr := resolveReasonixV4Payload(contentRoot, ev.Payload, ev.PayloadRef)
				if resolveErr != nil {
					return nil, resolveErr
				}
				replay.apply(ev.Kind, payload)
			}
			pending = nil
		}
	}
}

func readReasonixV4Frame(ctx context.Context, reader io.Reader, decoder *zstd.Decoder, compressedBuf, rawBuf []byte) ([]byte, []byte, []byte, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, compressedBuf, rawBuf, false, err
	}
	var header [reasonixV4FrameHeaderBytes]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, compressedBuf, rawBuf, false, nil
		}
		return nil, compressedBuf, rawBuf, false, err
	}
	if [4]byte(header[:4]) != reasonixV4FrameMagic {
		return nil, compressedBuf, rawBuf, false, fmt.Errorf("invalid Reasonix v4 frame magic")
	}
	compressedBytes := int(binary.BigEndian.Uint32(header[4:8]))
	rawBytes := int(binary.BigEndian.Uint32(header[8:12]))
	if compressedBytes <= 0 || compressedBytes > reasonixV4MaxFrameBytes || rawBytes <= 0 || rawBytes > reasonixV4MaxFrameBytes {
		return nil, compressedBuf, rawBuf, false, fmt.Errorf("invalid Reasonix v4 frame sizes compressed=%d raw=%d", compressedBytes, rawBytes)
	}
	if cap(compressedBuf) < compressedBytes {
		compressedBuf = make([]byte, compressedBytes)
	} else {
		compressedBuf = compressedBuf[:compressedBytes]
	}
	if _, err := io.ReadFull(reader, compressedBuf); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, compressedBuf, rawBuf, false, nil
		}
		return nil, compressedBuf, rawBuf, false, err
	}
	if cap(rawBuf) < rawBytes {
		rawBuf = make([]byte, 0, rawBytes)
	} else {
		rawBuf = rawBuf[:0]
	}
	raw, err := decoder.DecodeAll(compressedBuf, rawBuf)
	if err != nil {
		return nil, compressedBuf, rawBuf, false, fmt.Errorf("decode Reasonix v4 frame: %w", err)
	}
	rawBuf = raw
	if len(raw) != rawBytes {
		return nil, compressedBuf, rawBuf, false, fmt.Errorf("the Reasonix v4 frame decoded %d bytes, expected %d", len(raw), rawBytes)
	}
	return raw, compressedBuf, rawBuf, true, nil
}

func readReasonixV3EventsJSONL(ctx context.Context, eventsPath, contentRoot string) ([]reasonixJournalMessage, error) {
	file, info, err := openReasonixRegularFile(eventsPath, "Reasonix v3 event log")
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	var replay reasonixMessageReplay
	_, err = readReasonixJSONL(ctx, file, 0, info.Size(), func(raw json.RawMessage, _ int64, _ int) error {
		var commit reasonixV3Commit
		if json.Unmarshal(raw, &commit) != nil || commit.RecordType != "commit" {
			return nil
		}
		for _, ev := range commit.Events {
			if !isReasonixMessageEventKind(ev.Kind) {
				continue
			}
			payload, resolveErr := resolveReasonixV4Payload(contentRoot, ev.Payload, ev.PayloadRef)
			if resolveErr != nil {
				return resolveErr
			}
			replay.apply(ev.Kind, payload)
		}
		return nil
	})
	return replay.messages, err
}

func resolveReasonixV4Payload(contentRoot string, inline []byte, ref *reasonixV4ContentRef) ([]byte, error) {
	if ref == nil {
		return inline, nil
	}
	if contentRoot == "" || ref.Bytes < 0 || ref.Bytes > reasonixV4MaxFrameBytes || !isReasonixSHA256Hex(ref.Digest) {
		return nil, fmt.Errorf("invalid Reasonix content ref %q", ref.Digest)
	}
	objPath := filepath.Join(contentRoot, "objects", ref.Digest[:2], ref.Digest[2:4], ref.Digest)
	file, err := os.Open(objPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() != ref.Bytes {
		return nil, fmt.Errorf("the Reasonix content object %s size mismatched", ref.Digest)
	}
	data := make([]byte, int(ref.Bytes))
	if _, err := io.ReadFull(file, data); err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != ref.Digest {
		return nil, fmt.Errorf("the Reasonix content object %s checksum mismatched", ref.Digest)
	}
	return data, nil
}

func isReasonixSHA256Hex(digest string) bool {
	if len(digest) != sha256.Size*2 {
		return false
	}
	for _, c := range digest {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

type reasonixMessageReplay struct {
	messages []reasonixJournalMessage
	byID     map[string]int
}

func (r *reasonixMessageReplay) rebuildIndex() {
	if len(r.messages) == 0 {
		r.byID = nil
		return
	}
	r.byID = make(map[string]int, len(r.messages))
	for i, msg := range r.messages {
		if msg.ID != "" {
			r.byID[msg.ID] = i
		}
	}
}

func (r *reasonixMessageReplay) apply(kind string, payload []byte) {
	if len(payload) == 0 {
		return
	}
	switch kind {
	case "message/complete", "message/upsert":
		overwrite := kind == "message/upsert"
		var body struct {
			Message *reasonixJournalMessage `json:"message"`
		}
		if json.Unmarshal(payload, &body) != nil || body.Message == nil || body.Message.Role == "" {
			return
		}
		if body.Message.ID != "" {
			if idx, exists := r.byID[body.Message.ID]; exists {
				if overwrite {
					r.messages[idx] = *body.Message
				}
				return
			}
			if r.byID == nil {
				r.byID = make(map[string]int)
			}
			r.byID[body.Message.ID] = len(r.messages)
		}
		r.messages = append(r.messages, *body.Message)
	case "message/retract":
		var body struct {
			MessageIDs []string `json:"messageIds"`
		}
		if json.Unmarshal(payload, &body) != nil || len(body.MessageIDs) == 0 {
			return
		}
		removed := make(map[string]bool, len(body.MessageIDs))
		for _, id := range body.MessageIDs {
			if id != "" {
				removed[id] = true
			}
		}
		if len(removed) == 0 {
			return
		}
		r.messages = slices.DeleteFunc(r.messages, func(m reasonixJournalMessage) bool {
			return m.ID != "" && removed[m.ID]
		})
		r.rebuildIndex()
	case "history/replace", "legacy/import":
		var body struct {
			Messages []reasonixJournalMessage `json:"messages"`
		}
		if json.Unmarshal(payload, &body) != nil || body.Messages == nil {
			return
		}
		seen := make(map[string]bool, len(body.Messages))
		replaced := make([]reasonixJournalMessage, 0, len(body.Messages))
		for _, m := range body.Messages {
			if m.Role == "" {
				continue
			}
			if m.ID != "" {
				if seen[m.ID] {
					continue
				}
				seen[m.ID] = true
			}
			replaced = append(replaced, m)
		}
		r.messages = replaced
		r.rebuildIndex()
	}
}

func applyReasonixSessionEvent(messages *[]reasonixJournalMessage, kind string, payload []byte) {
	replay := reasonixMessageReplay{messages: *messages}
	replay.rebuildIndex()
	replay.apply(kind, payload)
	*messages = replay.messages
}
