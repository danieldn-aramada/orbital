package orbserver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// dgraphQuery sends a GraphQL query to orb's local DGraph.
func (s *Server) dgraphQuery(query string, variables map[string]any) ([]byte, error) {
	payload := map[string]any{"query": query}
	if variables != nil {
		payload["variables"] = variables
	}
	body, _ := json.Marshal(payload)
	resp, err := http.Post(s.cfg.DGraphURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("dgraph query: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	return raw, nil
}
