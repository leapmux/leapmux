package main

import (
	"io"

	desktoppb "github.com/leapmux/leapmux/generated/proto/leapmux/desktop/v1"
	"google.golang.org/protobuf/encoding/protodelim"
)

const maxFrameSize = 16 * 1024 * 1024 // 16 MB

func WriteFrame(w io.Writer, frame *desktoppb.Frame) error {
	_, err := protodelim.MarshalTo(w, frame)
	return err
}

func ReadFrame(r protodelim.Reader) (*desktoppb.Frame, error) {
	frame := &desktoppb.Frame{}
	err := protodelim.UnmarshalOptions{MaxSize: maxFrameSize}.UnmarshalFrom(r, frame)
	if err != nil {
		return nil, err
	}
	return frame, nil
}
