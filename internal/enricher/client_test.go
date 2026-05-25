package enricher

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClient_Enrich(t *testing.T) {
	fakePayload := []byte("fake-configbundle-manifest-yaml-content")
	fakeB64 := base64.StdEncoding.EncodeToString(fakePayload)

	tests := []struct {
		name        string
		handler     http.HandlerFunc
		opts        []ClientOption
		wantLayers  int
		wantData    []byte
		wantErrSub  string
	}{
		{
			name: "single layer returned",
			handler: func(w http.ResponseWriter, r *http.Request) {
				var req Request
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					http.Error(w, "bad request", http.StatusBadRequest)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode([]map[string]string{
					{
						"mediaType": "application/vnd.armada.configbundle.manifest.v1+yaml",
						"data":      fakeB64,
					},
				})
			},
			wantLayers: 1,
			wantData:   fakePayload,
		},
		{
			name: "empty array is valid",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte("[]"))
			},
			wantLayers: 0,
		},
		{
			name: "multiple layers returned",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode([]map[string]string{
					{"mediaType": "application/vnd.armada.configbundle.manifest.v1+yaml", "data": fakeB64},
					{"mediaType": "application/vnd.armada.other.v1+json", "data": base64.StdEncoding.EncodeToString([]byte("other"))},
				})
			},
			wantLayers: 2,
		},
		{
			name: "non-2xx response returns error",
			handler: func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "internal error", http.StatusInternalServerError)
			},
			wantErrSub: "HTTP 500",
		},
		{
			name: "404 returns error",
			handler: func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "not found", http.StatusNotFound)
			},
			wantErrSub: "HTTP 404",
		},
		{
			name: "response exceeds size cap",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				// Write more than the cap (set to 10 bytes via WithMaxResponseBytes).
				w.Write([]byte(`[{"mediaType":"x","data":"` + fakeB64 + `"}]`))
			},
			opts:       []ClientOption{WithMaxResponseBytes(10)},
			wantErrSub: "byte limit",
		},
		{
			name: "invalid base64 in data field",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(`[{"mediaType":"x","data":"!!!not-base64!!!"}]`))
			},
			wantErrSub: "decode layer data",
		},
		{
			name: "malformed json response",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(`not json`))
			},
			wantErrSub: "decode enricher response",
		},
		{
			name: "request body contains jobId and datacenter",
			handler: func(w http.ResponseWriter, r *http.Request) {
				var req Request
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					http.Error(w, "bad request", http.StatusBadRequest)
					return
				}
				if req.JobID != "test-job-id" || req.Datacenter != "colo-galleon" {
					http.Error(w, "unexpected request fields", http.StatusBadRequest)
					return
				}
				w.Write([]byte("[]"))
			},
			wantLayers: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(tt.handler)
			defer srv.Close()

			c := New(srv.URL, 5*time.Second, tt.opts...)
			layers, err := c.Enrich(context.Background(), Request{
				JobID:      "test-job-id",
				Datacenter: "colo-galleon",
			})

			if tt.wantErrSub != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErrSub)
				}
				if got := err.Error(); !strings.Contains(got, tt.wantErrSub) {
					t.Errorf("error %q does not contain %q", got, tt.wantErrSub)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(layers) != tt.wantLayers {
				t.Errorf("got %d layers, want %d", len(layers), tt.wantLayers)
			}
			if tt.wantData != nil && len(layers) > 0 {
				if string(layers[0].Data) != string(tt.wantData) {
					t.Errorf("layer[0].Data = %q, want %q", layers[0].Data, tt.wantData)
				}
			}
		})
	}
}

func TestClient_WithHTTPClient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("[]"))
	}))
	defer srv.Close()

	custom := &http.Client{Timeout: 2 * time.Second}
	c := New(srv.URL, 5*time.Second, WithHTTPClient(custom))
	if c.httpClient != custom {
		t.Error("WithHTTPClient did not replace backing client")
	}
	_, err := c.Enrich(context.Background(), Request{JobID: "j", Datacenter: "dc"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestClient_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.Write([]byte("[]"))
	}))
	defer srv.Close()

	c := New(srv.URL, 50*time.Millisecond) // timeout shorter than handler delay
	_, err := c.Enrich(context.Background(), Request{JobID: "j", Datacenter: "dc"})
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}

