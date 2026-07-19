package main

import (
	"fmt"
	"io"

	"github.com/leapmux/leapmux/channelwire"
	desktoppb "github.com/leapmux/leapmux/generated/proto/leapmux/desktop/v1"
	"google.golang.org/protobuf/encoding/protodelim"
	"google.golang.org/protobuf/proto"
)

// maxFrameSize bounds a single desktop RPC frame. It must exceed the largest
// payload the sidecar relays -- an org-events OrgMaterialized bootstrap up to
// channelwire.OrgEventsReadLimit -- PLUS its Frame/Event proto envelope, so a
// full-size bootstrap is forwarded rather than silently dropped by
// validateFrameSize. The 4 MiB margin covers the envelope with generous
// headroom. MAX_FRAME_SIZE in desktop/rust/src/main.rs must stay in sync.
const maxFrameSize = channelwire.OrgEventsReadLimit + 4*1024*1024 // 20 MiB

func WriteFrame(w io.Writer, frame *desktoppb.Frame) error {
	if err := validateFrameSize(frame); err != nil {
		return err
	}
	return writeFrameTo(w, frame)
}

// writeFrameTo marshals and writes frame WITHOUT enforcing the size budget.
// Callers that have already validated the frame -- e.g. RPCSession.writeResponse,
// which substitutes an in-budget error response on overflow -- use this to
// avoid walking the proto tree with proto.Size twice on the response hot path
// (once in the caller, once inside WriteFrame). WriteFrame remains the single
// size-validating entry point for callers that have not pre-validated.
func writeFrameTo(w io.Writer, frame *desktoppb.Frame) error {
	_, err := protodelim.MarshalTo(w, frame)
	return err
}

func validateFrameSize(frame *desktoppb.Frame) error {
	if size := proto.Size(frame); size > maxFrameSize {
		return fmt.Errorf("frame size %d exceeds %d-byte frame budget", size, maxFrameSize)
	}
	return nil
}

func ReadFrame(r protodelim.Reader) (*desktoppb.Frame, error) {
	frame := &desktoppb.Frame{}
	err := protodelim.UnmarshalOptions{MaxSize: maxFrameSize}.UnmarshalFrom(r, frame)
	if err != nil {
		return nil, err
	}
	return frame, nil
}
