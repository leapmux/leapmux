package cmd

import (
	"fmt"
	"io"
	"strings"

	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
)

type gitDiffResult struct {
	Diff      string `json:"diff"`
	Truncated bool   `json:"truncated"`
	TotalSize int64  `json:"total_size"`
}

func (r gitDiffResult) RenderText(w io.Writer) error {
	if r.Diff != "" {
		clean := cli.SanitizeTerminalText(r.Diff)
		if _, err := io.WriteString(w, clean); err != nil {
			return err
		}
		if !strings.HasSuffix(clean, "\n") {
			if _, err := fmt.Fprintln(w); err != nil {
				return err
			}
		}
	}
	if r.Truncated {
		if _, err := fmt.Fprintf(w, "\n(diff truncated at %d of %d bytes; pass --limit to show more)\n", len(r.Diff), r.TotalSize); err != nil {
			return err
		}
	}
	return nil
}

func runGitDiff(inv *cli.Invocation) (any, error) {
	if err := checkGitDirInput(inv); err != nil {
		return nil, err
	}
	limit, err := inv.Command.Flags().GetInt64("limit")
	if err != nil {
		return nil, err
	}
	if limit < 0 {
		return nil, cli.Usagef("--limit must not be negative")
	}
	var resp leapmuxv1.GetGitDiffResponse
	if err := callOnWorkerWithDir(inv, leapmuxv1connect.WorkerGitServiceGetGitDiffProcedure, func(dir string) proto.Message {
		return &leapmuxv1.GetGitDiffRequest{
			Path:      dir,
			Staged:    boolFlag(inv, "staged"),
			BaseRef:   stringFlag(inv, "base"),
			FilePaths: inv.Variadic("path"),
			Limit:     limit,
		}
	}, &resp); err != nil {
		return nil, err
	}
	return gitDiffResult{
		Diff:      resp.GetDiff(),
		Truncated: resp.GetTruncated(),
		TotalSize: resp.GetTotalSize(),
	}, nil
}

type gitCommitRow struct {
	Hash        string `json:"hash"`
	ShortHash   string `json:"short_hash" table:"HASH"`
	AuthorName  string `json:"author_name" table:"AUTHOR"`
	AuthorEmail string `json:"author_email,omitempty"`
	AuthoredAt  string `json:"authored_at" table:"DATE,dim"`
	Subject     string `json:"subject" table:"SUBJECT"`
}

type gitLogResult struct {
	Commits []gitCommitRow `json:"commits"`
}

func (r gitLogResult) QuietLines() []string {
	out := make([]string, 0, len(r.Commits))
	for _, c := range r.Commits {
		out = append(out, c.Hash)
	}
	return out
}

func runGitLog(inv *cli.Invocation) (any, error) {
	if err := checkGitDirInput(inv); err != nil {
		return nil, err
	}
	limit, err := inv.Command.Flags().GetInt("limit")
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 200 {
		return nil, cli.Usagef("--limit must be between 1 and 200")
	}
	var resp leapmuxv1.GetGitLogResponse
	if err := callOnWorkerWithDir(inv, leapmuxv1connect.WorkerGitServiceGetGitLogProcedure, func(dir string) proto.Message {
		return &leapmuxv1.GetGitLogRequest{
			Path:  dir,
			Limit: clampInt32(limit),
			Ref:   stringFlag(inv, "ref"),
		}
	}, &resp); err != nil {
		return nil, err
	}
	rows := make([]gitCommitRow, 0, len(resp.GetCommits()))
	for _, c := range resp.GetCommits() {
		rows = append(rows, gitCommitRow{
			Hash:        c.GetHash(),
			ShortHash:   c.GetShortHash(),
			AuthorName:  c.GetAuthorName(),
			AuthorEmail: c.GetAuthorEmail(),
			AuthoredAt:  c.GetAuthoredAt(),
			Subject:     c.GetSubject(),
		})
	}
	return gitLogResult{Commits: rows}, nil
}

type gitCommitResult struct {
	Hash    string `json:"hash"`
	Subject string `json:"subject"`
}

func (r gitCommitResult) QuietLines() []string {
	if r.Hash == "" {
		return nil
	}
	return []string{r.Hash}
}

func runGitCommit(inv *cli.Invocation) (any, error) {
	if err := checkGitDirInput(inv); err != nil {
		return nil, err
	}
	msg := strings.TrimSpace(stringFlag(inv, "message"))
	if msg == "" {
		return nil, cli.Usagef("--message is empty")
	}
	var resp leapmuxv1.GitCommitResponse
	if err := callOnWorkerWithDir(inv, leapmuxv1connect.WorkerGitServiceGitCommitProcedure, func(dir string) proto.Message {
		return &leapmuxv1.GitCommitRequest{
			Path:     dir,
			Message:  msg,
			StageAll: boolFlag(inv, "all"),
		}
	}, &resp); err != nil {
		return nil, err
	}
	return gitCommitResult{
		Hash:    resp.GetHash(),
		Subject: resp.GetSubject(),
	}, nil
}
