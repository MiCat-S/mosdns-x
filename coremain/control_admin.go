package coremain

import (
	"context"

	"github.com/pmkol/mosdns-x/internal/control"
)

func hasEnabledAdministrator(ctx context.Context, store control.Service) (bool, error) {
	cursor := ""
	for {
		page, err := store.ListUsers(ctx, control.Page{Limit: 1000, Cursor: cursor})
		if err != nil {
			return false, err
		}
		for _, user := range page.Items {
			if user.Role == control.RoleAdmin && user.Enabled {
				return true, nil
			}
		}
		if page.NextCursor == "" {
			return false, nil
		}
		cursor = page.NextCursor
	}
}
