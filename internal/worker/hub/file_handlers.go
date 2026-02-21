package hub

import (
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/filebrowser"
)

func (c *Client) handleFileBrowse(requestID string, req *leapmuxv1.FileBrowseRequest) {
	path, entries, err := filebrowser.ListDirectory(req.GetPath())
	resp := &leapmuxv1.FileBrowseResponse{Path: path}

	if err != nil {
		resp.Error = err.Error()
	} else {
		resp.Entries = make([]*leapmuxv1.FileEntry, len(entries))
		for i, e := range entries {
			resp.Entries[i] = &leapmuxv1.FileEntry{
				Name:        e.Name,
				IsDir:       e.IsDir,
				Size:        e.Size,
				ModTime:     e.ModTime,
				Permissions: e.Permissions,
			}
		}
	}

	_ = c.Send(&leapmuxv1.ConnectRequest{
		RequestId: requestID,
		Payload: &leapmuxv1.ConnectRequest_FileBrowseResp{
			FileBrowseResp: resp,
		},
	})
}

func (c *Client) handleFileRead(requestID string, req *leapmuxv1.FileReadRequest) {
	path, content, totalSize, err := filebrowser.ReadFile(req.GetPath(), req.GetOffset(), req.GetLimit())
	resp := &leapmuxv1.FileReadResponse{Path: path}

	if err != nil {
		resp.Error = err.Error()
	} else {
		resp.Content = content
		resp.TotalSize = totalSize
	}

	_ = c.Send(&leapmuxv1.ConnectRequest{
		RequestId: requestID,
		Payload: &leapmuxv1.ConnectRequest_FileReadResp{
			FileReadResp: resp,
		},
	})
}

func (c *Client) handleFileStat(requestID string, req *leapmuxv1.FileStatRequest) {
	path, entry, err := filebrowser.StatFile(req.GetPath())
	resp := &leapmuxv1.FileStatResponse{Path: path}

	if err != nil {
		resp.Error = err.Error()
	} else {
		resp.Entry = &leapmuxv1.FileEntry{
			Name:        entry.Name,
			IsDir:       entry.IsDir,
			Size:        entry.Size,
			ModTime:     entry.ModTime,
			Permissions: entry.Permissions,
		}
	}

	_ = c.Send(&leapmuxv1.ConnectRequest{
		RequestId: requestID,
		Payload: &leapmuxv1.ConnectRequest_FileStatResp{
			FileStatResp: resp,
		},
	})
}
