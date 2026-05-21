package handler

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestToFloat64(t *testing.T) {
	tests := []struct {
		name  string
		input any
		want  float64
	}{
		{name: "float64", input: float64(3.14), want: 3.14},
		{name: "zero float64", input: float64(0), want: 0},
		{name: "int", input: int(7), want: 7},
		{name: "json.Number integer", input: json.Number("42"), want: 42},
		{name: "json.Number float", input: json.Number("1.5"), want: 1.5},
		{name: "nil returns zero", input: nil, want: 0},
		{name: "string returns zero", input: "not a number", want: 0},
		{name: "bool returns zero", input: true, want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := toFloat64(tt.input)
			if got != tt.want {
				t.Errorf("toFloat64(%v) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestExtractResourceIDs(t *testing.T) {
	tests := []struct {
		name      string
		query     string
		variables map[string]any
		respBody  []byte
		want      []string
	}{
		{
			name:      "single orbId in variables",
			query:     `mutation { updateServer(input: {}) { server { id } } }`,
			variables: map[string]any{"orbId": "alaska:SRV001"},
			respBody:  []byte(`{}`),
			want:      []string{"alaska:SRV001"},
		},
		{
			name:  "orbId in input array",
			query: `mutation { addServer(input: []) { server { id } } }`,
			variables: map[string]any{
				"input": []any{
					map[string]any{"orbId": "alaska:SRV001"},
					map[string]any{"orbId": "alaska:SRV002"},
				},
			},
			respBody: []byte(`{}`),
			want:     []string{"alaska:SRV001", "alaska:SRV002"},
		},
		{
			name:      "orbId in inline filter expression",
			query:     `mutation { updateServer(filter: { orbId: { eq: "alaska:SRV003" } }, set: {}) { server { id } } }`,
			variables: map[string]any{},
			respBody:  []byte(`{}`),
			want:      []string{"alaska:SRV003"},
		},
		{
			name:      "orbId in response body",
			query:     `mutation { addServer(input: []) { server { orbId } } }`,
			variables: map[string]any{},
			respBody:  []byte(`{"data":{"addServer":{"server":[{"orbId":"alaska:SRV004"}]}}}`),
			want:      []string{"alaska:SRV004"},
		},
		{
			name:  "deduplicated across all sources",
			query: `mutation { updateServer(filter: { orbId: { eq: "alaska:SRV001" } }, set: {}) { server { orbId } } }`,
			variables: map[string]any{
				"orbId": "alaska:SRV001",
			},
			respBody: []byte(`{"data":{"updateServer":{"server":[{"orbId":"alaska:SRV001"}]}}}`),
			want:     []string{"alaska:SRV001"},
		},
		{
			name:      "empty variables and body returns empty",
			query:     `mutation { addServer(input: []) { server { id } } }`,
			variables: map[string]any{},
			respBody:  []byte(`{"data":{}}`),
			want:      nil,
		},
		{
			name:      "nested orbIds in response collected recursively",
			query:     `mutation { addDataCenter(input: []) { dataCenter { orbId servers { orbId } } } }`,
			variables: map[string]any{},
			respBody: []byte(`{"data":{"addDataCenter":{"dataCenter":[{
				"orbId":"alaska",
				"servers":[{"orbId":"alaska:SRV001"},{"orbId":"alaska:SRV002"}]
			}]}}}`),
			want: []string{"alaska", "alaska:SRV001", "alaska:SRV002"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractResourceIDs(tt.query, tt.variables, tt.respBody)
			if !slices.Equal(got, tt.want) {
				t.Errorf("extractResourceIDs() = %v, want %v", got, tt.want)
			}
		})
	}
}

