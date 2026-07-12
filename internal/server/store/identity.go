package store

import (
	"context"
	"fmt"
)

// ServerInstanceID returns the durable credential-realm identity owned by the
// database. It is independent from listener and public URL configuration so a
// restored process keeps its realm while a fresh database can never inherit
// credentials merely by reusing an origin.
func (s *Store) ServerInstanceID(ctx context.Context) (string, error) {
	var id string
	if err := s.pool.QueryRow(ctx, `SELECT instance_id::text FROM server_instance_identity WHERE singleton`).Scan(&id); err != nil {
		return "", fmt.Errorf("store: load server instance identity: %w", err)
	}
	if id == "" {
		return "", fmt.Errorf("store: server instance identity is empty")
	}
	return "issue-spec:" + id, nil
}
