package main

import (
	"context"
	"flag"
	"fmt"
	"time"

	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/timefmt"
)

func runOrgList(cmd adminCmdCtx, args []string) error {
	var query *string
	var limit *int64
	var cursor *string
	return withAdminStore(cmd, args, func(fs *flag.FlagSet) {
		query = fs.String("query", "", "search query (prefix match on org name)")
		limit = fs.Int64("limit", 50, "maximum number of results")
		cursor = fs.String("cursor", "", "cursor for pagination (created_at in RFC3339Nano)")
	}, func(ctx context.Context, _ *config.Config, st store.Store) error {
		var orgs []store.Org
		var err error

		if *query != "" {
			orgs, err = st.Orgs().Search(ctx, store.SearchOrgsParams{
				Query:  query,
				Limit:  *limit,
				Cursor: *cursor,
			})
		} else {
			orgs, err = st.Orgs().ListAll(ctx, store.ListAllOrgsParams{
				Limit:  *limit,
				Cursor: *cursor,
			})
		}
		if err != nil {
			return fmt.Errorf("list orgs: %w", err)
		}

		if len(orgs) == 0 {
			fmt.Println("No organizations found.")
			return nil
		}

		fmt.Printf("%-48s %-30s %-10s %-24s\n", "ID", "NAME", "PERSONAL", "CREATED")
		for _, o := range orgs {
			fmt.Printf("%-48s %-30s %-10s %-24s\n",
				o.ID, o.Name, yesNo(o.IsPersonal), timefmt.Format(o.CreatedAt))
		}

		maybePrintNextCursor(orgs, *limit, func(o store.Org) time.Time { return o.CreatedAt })
		return nil
	})
}
