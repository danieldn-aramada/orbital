package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/armada/orbital/internal/web/data/layout"
	"github.com/labstack/echo/v4"
)

func TestServerTab_NonHTMX_Redirects(t *testing.T) {
	t.Chdir("../..")
	h := NewServerHandler("http://localhost:8080/graphql", false, slog.Default(), "/app", func(echo.Context) layout.PageActions { return layout.OrbActions })

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues("0x1")

	if err := h.Tab(c); err != nil {
		t.Fatalf("Tab: %v", err)
	}
	if rec.Code != http.StatusFound {
		t.Errorf("expected 302, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/app/" {
		t.Errorf("Location: got %q, want /app/", loc)
	}
}

func TestServerTab_DGraphUnreachable(t *testing.T) {
	t.Chdir("../..")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	srv.Close()

	h := NewServerHandler(srv.URL, false, slog.Default(), "/app", func(echo.Context) layout.PageActions { return layout.OrbActions })

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues("0x1")

	if err := h.Tab(c); err == nil {
		t.Error("expected error when DGraph is unreachable, got nil")
	}
}

func TestServerTab_DGraphDecodeError(t *testing.T) {
	t.Chdir("../..")
	dgraph := newDGraphStub(t, "not json")

	h := NewServerHandler(dgraph.URL, false, slog.Default(), "/app", func(echo.Context) layout.PageActions { return layout.OrbActions })

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues("0x1")

	if err := h.Tab(c); err == nil {
		t.Error("expected error on JSON decode failure, got nil")
	}
}

func TestServerTab_Success(t *testing.T) {
	t.Chdir("../..")

	body, _ := json.Marshal(map[string]any{
		"data": map[string]any{
			"getServer": map[string]any{
				"id":           "0x2",
				"name":         "srv-01",
				"orbId":        "test:srv-01",
				"hostname":     "srv-01.example.com",
				"rackPosition": 1,
				"namespace":    "test-ns",
				"rack":         map[string]any{"id": "0x3", "name": "rack-a"},
				"dataCenter":   map[string]any{"id": "0x1", "name": "Test DC"},
				"oobIP":        map[string]any{"orbId": "test:srv-01-oobip", "address": "10.0.0.1", "role": "oob"},
				"idracSettings": map[string]any{
					"orbId":           "test:srv-01-idrac",
					"firmwareVersion": "7.0.0",
					"sshEnabled":      true,
				},
				"serverConfigurationProfile": map[string]any{
					"orbId": "test:srv-01-scp",
					"json":  `{"foo":"bar"}`,
				},
				"storageControllers": []any{
					map[string]any{"orbId": "test:srv-01-ctrl-0", "name": "PERC H755"},
				},
			},
		},
	})
	dgraph := newDGraphStub(t, string(body))

	h := NewServerHandler(dgraph.URL, false, slog.Default(), "/app", func(echo.Context) layout.PageActions { return layout.OrbitalActions(false) })

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues("0x2")

	if err := h.Tab(c); err != nil {
		t.Fatalf("Tab: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("Content-Type: got %q, want text/html", ct)
	}
	// Audit-tab li must carry the full subgraph orbId list so the panel can
	// fetch events for the server AND its nested ConfigItems in one call.
	wantCSV := `data-related-orb-ids="test:srv-01,test:srv-01-idrac,test:srv-01-scp,test:srv-01-oobip,test:srv-01-ctrl-0"`
	if !strings.Contains(rec.Body.String(), wantCSV) {
		t.Errorf("server tab missing %q in rendered body", wantCSV)
	}
}

func TestCollectRelatedOrbIDs(t *testing.T) {
	mk := func(server string, idrac, scp, oob string, controllers ...string) *serverQueryResponse {
		r := &serverQueryResponse{OrbID: server}
		r.OobIP.OrbID = oob
		if idrac != "" {
			r.IdracSettings = &struct {
				OrbID                       string `json:"orbId"`
				Version                     int    `json:"version"`
				FirmwareVersion             string `json:"firmwareVersion"`
				OsToIdracPassThroughEnabled bool   `json:"osToIdracPassThroughEnabled"`
				SshEnabled                  bool   `json:"sshEnabled"`
				UsbManagementPortEnabled    bool   `json:"usbManagementPortEnabled"`
				IpmiEnabled                 bool   `json:"ipmiEnabled"`
				LockdownModeEnabled         bool   `json:"lockdownModeEnabled"`
				DhcpEnabled                 bool   `json:"dhcpEnabled"`
				RacadmEnabled               bool   `json:"racadmEnabled"`
			}{OrbID: idrac}
		}
		if scp != "" {
			r.ServerConfigurationProfile = &struct {
				OrbID string `json:"orbId"`
				JSON  string `json:"json"`
			}{OrbID: scp}
		}
		for _, c := range controllers {
			r.StorageControllers = append(r.StorageControllers, struct {
				OrbID          string `json:"orbId"`
				Name           string `json:"name"`
				StorageDevices []struct {
					Name          string `json:"name"`
					CapacityBytes int    `json:"capacityBytes"`
					Manufacturer  string `json:"manufacturer"`
					SerialNumber  string `json:"serialNumber"`
					WWN           string `json:"wwn"`
				} `json:"storageDevices"`
			}{OrbID: c})
		}
		return r
	}

	cases := []struct {
		name string
		in   *serverQueryResponse
		want []string
	}{
		{
			name: "full subgraph",
			in:   mk("srv", "idrac", "scp", "oob", "ctrl0", "ctrl1"),
			want: []string{"srv", "idrac", "scp", "oob", "ctrl0", "ctrl1"},
		},
		{
			name: "no idrac no scp no controllers",
			in:   mk("srv", "", "", "oob"),
			want: []string{"srv", "oob"},
		},
		{
			name: "missing oob orbId is skipped",
			in:   mk("srv", "idrac", "", ""),
			want: []string{"srv", "idrac"},
		},
		{
			name: "server only",
			in:   mk("srv", "", "", ""),
			want: []string{"srv"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := collectRelatedOrbIDs(tc.in)
			if !slices.Equal(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}
