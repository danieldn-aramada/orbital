package bundler

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
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
		name       string
		handler    http.HandlerFunc
		opts       []ClientOption
		wantLayers int
		wantData   []byte
		wantErrSub string
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
				json.NewEncoder(w).Encode(map[string]any{
					"layers": []map[string]string{
						{
							"mediaType": "application/vnd.armada.configbundle.manifest.v1+yaml",
							"data":      fakeB64,
						},
					},
				})
			},
			wantLayers: 1,
			wantData:   fakePayload,
		},
		{
			name: "empty layers list is valid",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(`{"layers":[]}`))
			},
			wantLayers: 0,
		},
		{
			name: "multiple layers returned",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]any{
					"layers": []map[string]string{
						{"mediaType": "application/vnd.armada.configbundle.manifest.v1+yaml", "data": fakeB64},
						{"mediaType": "application/vnd.armada.other.v1+json", "data": base64.StdEncoding.EncodeToString([]byte("other"))},
					},
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
				w.Write([]byte(`{"layers":[{"mediaType":"x","data":"` + fakeB64 + `"}]}`))
			},
			opts:       []ClientOption{WithMaxResponseBytes(10)},
			wantErrSub: "byte limit",
		},
		{
			name: "invalid base64 in data field",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(`{"layers":[{"mediaType":"x","data":"!!!not-base64!!!"}]}`))
			},
			wantErrSub: "decode layer data",
		},
		{
			name: "malformed json response",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(`not json`))
			},
			wantErrSub: "decode bundler response",
		},
		{
			name: "request body contains orbId",
			handler: func(w http.ResponseWriter, r *http.Request) {
				var req Request
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					http.Error(w, "bad request", http.StatusBadRequest)
					return
				}
				if req.OrbID != "colo:colo-galleon" {
					http.Error(w, "unexpected request fields", http.StatusBadRequest)
					return
				}
				w.Write([]byte(`{"layers":[]}`))
			},
			wantLayers: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(tt.handler)
			defer srv.Close()

			c := New("test-bundler", srv.URL, 5*time.Second, tt.opts...)
			result, err := c.Enrich(context.Background(), Request{
				OrbID: "colo:colo-galleon",
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
			if len(result.Layers) != tt.wantLayers {
				t.Errorf("got %d layers, want %d", len(result.Layers), tt.wantLayers)
			}
			if tt.wantData != nil && len(result.Layers) > 0 {
				if string(result.Layers[0].Data) != string(tt.wantData) {
					t.Errorf("layer[0].Data = %q, want %q", result.Layers[0].Data, tt.wantData)
				}
			}
		})
	}
}

func TestClient_WithHTTPClient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"layers":[]}`))
	}))
	defer srv.Close()

	custom := &http.Client{Timeout: 2 * time.Second}
	c := New("test-bundler", srv.URL, 5*time.Second, WithHTTPClient(custom))
	if c.httpClient != custom {
		t.Error("WithHTTPClient did not replace backing client")
	}
	_, err := c.Enrich(context.Background(), Request{OrbID: "ns:dc"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestClient_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.Write([]byte(`{"layers":[]}`))
	}))
	defer srv.Close()

	c := New("test-bundler", srv.URL, 50*time.Millisecond) // timeout shorter than handler delay
	_, err := c.Enrich(context.Background(), Request{OrbID: "ns:dc"})
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}

// Orbital turns these two cases into DIFFERENT metric labels, and the label is
// what tells an operator whether to look at the network or at the bundler. The
// regression guarded here is a wrap that drops the sentinel — every message
// still reads correctly, so nothing looks wrong until the panel has quietly
// been reporting "internal" for weeks.
func TestClient_Enrich_ClassifiesFailures(t *testing.T) {
	t.Run("no answer at all is unreachable", func(t *testing.T) {
		// Port 1 on loopback: nothing listens, so Do() fails before any HTTP.
		c := New("test", "http://127.0.0.1:1/bundle", 2*time.Second)
		_, err := c.Enrich(context.Background(), Request{OrbID: "ns:dc"})
		if err == nil {
			t.Fatal("expected an error")
		}
		if !errors.Is(err, ErrUnreachable) {
			t.Errorf("err = %v; want it to satisfy ErrUnreachable", err)
		}
		if errors.Is(err, ErrRejected) {
			t.Error("an unreachable bundler must not also read as a rejection")
		}
	})

	t.Run("a non-2xx answer is a rejection", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, `oauth2: "unauthorized_client"`, http.StatusInternalServerError)
		}))
		defer srv.Close()

		c := New("test", srv.URL, 2*time.Second)
		_, err := c.Enrich(context.Background(), Request{OrbID: "ns:dc"})
		if err == nil {
			t.Fatal("expected an error")
		}
		if !errors.Is(err, ErrRejected) {
			t.Errorf("err = %v; want it to satisfy ErrRejected", err)
		}
		// The operator-facing text must survive classification — it names the
		// actual problem, which a category label never can.
		if !strings.Contains(err.Error(), "unauthorized_client") {
			t.Errorf("the bundler's own message was lost: %v", err)
		}
	})
}
