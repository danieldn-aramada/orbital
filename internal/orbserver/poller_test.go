package orbserver

import "testing"

func TestHighestVersionTag(t *testing.T) {
	cases := []struct {
		name      string
		tags      []string
		wantTag   string
		wantN     int
		wantFound bool
	}{
		{
			name:    "alphabetical from registry",
			tags:    []string{"latest", "v1", "v2", "v3"},
			wantTag: "v3", wantN: 3, wantFound: true,
		},
		{
			name:    "insertion order from registry",
			tags:    []string{"v3", "v2", "v1", "latest"},
			wantTag: "v3", wantN: 3, wantFound: true,
		},
		{
			name:    "v10 > v9 (numeric)",
			tags:    []string{"v1", "v2", "v9", "v10", "latest"},
			wantTag: "v10", wantN: 10, wantFound: true,
		},
		{
			name:    "cosign signature tags ignored",
			tags:    []string{"v3", "sha256-abc123.sig", "v2"},
			wantTag: "v3", wantN: 3, wantFound: true,
		},
		{
			name:    "no version tags",
			tags:    []string{"latest", "stable"},
			wantTag: "", wantN: 0, wantFound: false,
		},
		{
			name:    "empty input",
			tags:    nil,
			wantTag: "", wantN: 0, wantFound: false,
		},
		{
			name:    "user-reported scenario: at v3 with v4 published",
			tags:    []string{"latest", "v1", "v2", "v3", "v4"},
			wantTag: "v4", wantN: 4, wantFound: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotTag, gotN, gotFound := highestVersionTag(tc.tags)
			if gotTag != tc.wantTag || gotN != tc.wantN || gotFound != tc.wantFound {
				t.Errorf("got (%q, %d, %v), want (%q, %d, %v)",
					gotTag, gotN, gotFound, tc.wantTag, tc.wantN, tc.wantFound)
			}
		})
	}
}
