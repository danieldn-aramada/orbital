package handler

import (
	"testing"
	"time"

	"github.com/armada/orbital/ent"
	"github.com/armada/orbital/ent/backup"
	"github.com/armada/orbital/ent/exportjob"
	"github.com/armada/orbital/ent/registryartifact"
	"github.com/armada/orbital/ent/restorejob"
	"github.com/armada/orbital/internal/ocitype"
	"github.com/google/uuid"
)

// ── toBackupFragRow ───────────────────────────────────────────────────────────

func TestToBackupFragRow_StatusClass(t *testing.T) {
	cases := []struct {
		status    backup.Status
		wantClass string
	}{
		{backup.StatusCompleted, "is-success"},
		{backup.StatusRunning, "is-warning"},
		{backup.StatusPending, "is-warning"},
		{backup.StatusFailed, "is-danger"},
	}
	for _, c := range cases {
		j := &ent.Backup{ID: uuid.New(), Status: c.status, Trigger: backup.TriggerManual, CreatedAt: time.Now()}
		row := toBackupFragRow(j)
		if row.StatusClass != c.wantClass {
			t.Errorf("status %q: StatusClass = %q, want %q", c.status, row.StatusClass, c.wantClass)
		}
	}
}

func TestToBackupFragRow_ChecksumShortTruncates(t *testing.T) {
	long := "sha256:abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234"
	j := &ent.Backup{ID: uuid.New(), Status: backup.StatusCompleted, Trigger: backup.TriggerManual, CreatedAt: time.Now(), Checksum: long}
	row := toBackupFragRow(j)
	if len(row.ChecksumShort) != 36 {
		t.Errorf("ChecksumShort length = %d, want 36", len(row.ChecksumShort))
	}
	if row.Checksum != long {
		t.Error("Checksum should be the full original string")
	}
}

func TestToBackupFragRow_ChecksumShortKeepsShort(t *testing.T) {
	short := "abc123"
	j := &ent.Backup{ID: uuid.New(), Status: backup.StatusCompleted, Trigger: backup.TriggerManual, CreatedAt: time.Now(), Checksum: short}
	row := toBackupFragRow(j)
	if row.ChecksumShort != short {
		t.Errorf("ChecksumShort = %q, want %q", row.ChecksumShort, short)
	}
}

func TestToBackupFragRow_CanDownload(t *testing.T) {
	s3Key := "backups/foo.tar"
	j := &ent.Backup{ID: uuid.New(), Status: backup.StatusCompleted, Trigger: backup.TriggerManual, CreatedAt: time.Now(), S3Key: s3Key}
	if !toBackupFragRow(j).CanDownload {
		t.Error("expected CanDownload=true for completed job with S3Key")
	}

	j2 := &ent.Backup{ID: uuid.New(), Status: backup.StatusFailed, Trigger: backup.TriggerManual, CreatedAt: time.Now()}
	if toBackupFragRow(j2).CanDownload {
		t.Error("expected CanDownload=false for failed job")
	}
}

func TestToBackupFragRow_CanDelete(t *testing.T) {
	for _, status := range []backup.Status{backup.StatusRunning, backup.StatusPending} {
		j := &ent.Backup{ID: uuid.New(), Status: status, Trigger: backup.TriggerManual, CreatedAt: time.Now()}
		if toBackupFragRow(j).CanDelete {
			t.Errorf("expected CanDelete=false for status %q", status)
		}
	}
	for _, status := range []backup.Status{backup.StatusCompleted, backup.StatusFailed} {
		j := &ent.Backup{ID: uuid.New(), Status: status, Trigger: backup.TriggerManual, CreatedAt: time.Now()}
		if !toBackupFragRow(j).CanDelete {
			t.Errorf("expected CanDelete=true for status %q", status)
		}
	}
}

func TestToBackupFragRow_ErrorField(t *testing.T) {
	errMsg := "disk full"
	j := &ent.Backup{ID: uuid.New(), Status: backup.StatusFailed, Trigger: backup.TriggerManual, CreatedAt: time.Now(), Error: &errMsg}
	row := toBackupFragRow(j)
	if row.StatusError != errMsg {
		t.Errorf("StatusError = %q, want %q", row.StatusError, errMsg)
	}
}

// ── fmtBytes ──────────────────────────────────────────────────────────────────

func TestFmtBytes(t *testing.T) {
	cases := []struct {
		input *int64
		want  string
	}{
		{nil, "—"},
		{ptr64(0), "—"},
		{ptr64(512), "512 B"},
		{ptr64(1024), "1.0 KB"},
		{ptr64(1536), "1.5 KB"},
		{ptr64(1048576), "1.0 MB"},
		{ptr64(2097152), "2.0 MB"},
	}
	for _, c := range cases {
		got := fmtBytes(c.input)
		if got != c.want {
			t.Errorf("fmtBytes(%v) = %q, want %q", c.input, got, c.want)
		}
	}
}

func ptr64(n int64) *int64 { return &n }

// ── toExportJobFragRow ────────────────────────────────────────────────────────

func TestToExportJobFragRow_StatusClass(t *testing.T) {
	cases := []struct {
		status    exportjob.Status
		wantClass string
	}{
		{exportjob.StatusPending, "is-warning is-light"},
		{exportjob.StatusRunning, "is-info is-light"},
		{exportjob.StatusCompleted, "is-success is-light"},
		{exportjob.StatusFailed, "is-danger is-light"},
		{exportjob.StatusStale, "is-light"},
	}
	for _, c := range cases {
		job := &ent.ExportJob{ID: uuid.New(), Status: c.status, DatacenterName: "dc1", CreatedAt: time.Now()}
		row := toExportJobFragRow(job, false)
		if row.StatusClass != c.wantClass {
			t.Errorf("status %q: StatusClass = %q, want %q", c.status, row.StatusClass, c.wantClass)
		}
	}
}

func TestToExportJobFragRow_CompletedAt(t *testing.T) {
	ts := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	job := &ent.ExportJob{
		ID: uuid.New(), Status: exportjob.StatusCompleted,
		DatacenterName: "dc1", CreatedAt: time.Now(), CompletedAt: &ts,
	}
	row := toExportJobFragRow(job, false)
	if row.CompletedAt == "—" {
		t.Error("expected non-dash CompletedAt when set")
	}
}

func TestToExportJobFragRow_CanDownloadAndPublish(t *testing.T) {
	path := "/tmp/export.gz"
	job := &ent.ExportJob{
		ID: uuid.New(), Status: exportjob.StatusCompleted,
		DatacenterName: "dc1", CreatedAt: time.Now(), ArtifactPath: &path,
	}
	row := toExportJobFragRow(job, false)
	if !row.CanDownload {
		t.Error("expected CanDownload=true for completed job with ArtifactPath")
	}
	if !row.CanPublish {
		t.Error("expected CanPublish=true for completed job with ArtifactPath")
	}
}

func TestToExportJobFragRow_Published(t *testing.T) {
	job := &ent.ExportJob{ID: uuid.New(), Status: exportjob.StatusCompleted, DatacenterName: "dc1", CreatedAt: time.Now()}
	if !toExportJobFragRow(job, true).Published {
		t.Error("expected Published=true")
	}
}

// ── toArtifactFragRow ─────────────────────────────────────────────────────────

func TestToArtifactFragRow_NoDigest(t *testing.T) {
	a := &ent.RegistryArtifact{Status: registryartifact.StatusCompleted, InitiatedAt: time.Now()}
	row := toArtifactFragRow(a, "")
	if row.HasDigest {
		t.Error("expected HasDigest=false when Digest is nil")
	}
}

func TestToArtifactFragRow_DigestShort(t *testing.T) {
	d := "sha256:abcdef1234567890abcdef123456789012345678"
	a := &ent.RegistryArtifact{Status: registryartifact.StatusCompleted, InitiatedAt: time.Now(), Digest: &d}
	row := toArtifactFragRow(a, "")
	if !row.HasDigest {
		t.Error("expected HasDigest=true")
	}
	if len(row.DigestShort) != 19 {
		t.Errorf("DigestShort length = %d, want 19", len(row.DigestShort))
	}
}

func TestToArtifactFragRow_StatusClass(t *testing.T) {
	cases := []struct {
		status    registryartifact.Status
		wantClass string
	}{
		{registryartifact.StatusPending, "is-warning is-light"},
		{registryartifact.StatusPushing, "is-info is-light"},
		{registryartifact.StatusCompleted, "is-success is-light"},
		{registryartifact.StatusFailed, "is-danger is-light"},
	}
	for _, c := range cases {
		a := &ent.RegistryArtifact{Status: c.status, InitiatedAt: time.Now()}
		row := toArtifactFragRow(a, "")
		if row.StatusClass != c.wantClass {
			t.Errorf("status %q: StatusClass = %q, want %q", c.status, row.StatusClass, c.wantClass)
		}
	}
}

func TestToArtifactFragRow_Layers(t *testing.T) {
	a := &ent.RegistryArtifact{
		Status:      registryartifact.StatusCompleted,
		InitiatedAt: time.Now(),
		Layers: []ocitype.ArtifactLayer{
			{MediaType: "application/vnd.orbital.subgraph.data.v1+gzip", SizeBytes: 1024, Digest: "sha256:abc123", IsOrbitalNative: true},
			{MediaType: "application/vnd.orbital.subgraph.schema.v1+gzip", SizeBytes: 512, Digest: "sha256:def456", IsOrbitalNative: true},
			{MediaType: "application/vnd.example.config.v1+json", SizeBytes: 2048, Digest: "sha256:xyz789", IsOrbitalNative: false},
		},
	}
	row := toArtifactFragRow(a, "")

	if !row.HasLayers {
		t.Fatal("expected HasLayers=true")
	}
	if len(row.LayerRows) != 3 {
		t.Fatalf("expected 3 LayerRows, got %d", len(row.LayerRows))
	}

	// LayerRows are reversed from manifest order — bundler layers at top,
	// dgraph layers at bottom, matching container-image convention.
	if row.LayerRows[0].IsOrbitalNative {
		t.Error("LayerRows[0].IsOrbitalNative = true, want false (bundler at top)")
	}
	if row.LayerRows[0].MediaType != "application/vnd.example.config.v1+json" {
		t.Errorf("LayerRows[0].MediaType = %q, want bundler config", row.LayerRows[0].MediaType)
	}
	if !row.LayerRows[1].IsOrbitalNative || !row.LayerRows[2].IsOrbitalNative {
		t.Error("LayerRows[1] and LayerRows[2] should be orbital-native (dgraph)")
	}

	// SizeDisplay should be formatted — LayerRows[0] is the 2048-byte bundler layer.
	if row.LayerRows[0].SizeDisplay != "2.0 KB" {
		t.Errorf("LayerRows[0].SizeDisplay = %q, want %q", row.LayerRows[0].SizeDisplay, "2.0 KB")
	}
	if row.LayerRows[2].SizeDisplay != "1.0 KB" {
		t.Errorf("LayerRows[2].SizeDisplay = %q, want %q", row.LayerRows[2].SizeDisplay, "1.0 KB")
	}

	// DigestShort should be truncated.
	if row.LayerRows[0].DigestShort == "" {
		t.Error("LayerRows[0].DigestShort is empty")
	}
}

func TestToArtifactFragRow_NoLayers(t *testing.T) {
	a := &ent.RegistryArtifact{Status: registryartifact.StatusCompleted, InitiatedAt: time.Now()}
	row := toArtifactFragRow(a, "")
	if row.HasLayers {
		t.Error("expected HasLayers=false for artifact with no layers (legacy)")
	}
	if len(row.LayerRows) != 0 {
		t.Errorf("expected empty LayerRows, got %d", len(row.LayerRows))
	}
}

// ── toRestoreFragRow ──────────────────────────────────────────────────────────

func TestToRestoreFragRow_StatusClass(t *testing.T) {
	cases := []struct {
		status    restorejob.Status
		wantClass string
	}{
		{restorejob.StatusCompleted, "is-success"},
		{restorejob.StatusFailed, "is-danger"},
		{restorejob.StatusRunning, "is-warning"},
		{restorejob.StatusPending, "is-light"},
	}
	for _, c := range cases {
		j := &ent.RestoreJob{ID: uuid.New(), Status: c.status, CreatedAt: time.Now()}
		row := toRestoreFragRow(j)
		if row.StatusClass != c.wantClass {
			t.Errorf("status %q: StatusClass = %q, want %q", c.status, row.StatusClass, c.wantClass)
		}
	}
}

func TestToRestoreFragRow_Duration(t *testing.T) {
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(42 * time.Second)
	j := &ent.RestoreJob{
		ID: uuid.New(), Status: restorejob.StatusCompleted,
		CreatedAt: time.Now(), StartedAt: &start, CompletedAt: &end,
	}
	row := toRestoreFragRow(j)
	if row.Duration != "42s" {
		t.Errorf("Duration = %q, want %q", row.Duration, "42s")
	}
}

func TestToRestoreFragRow_DurationRunning(t *testing.T) {
	start := time.Now().Add(-10 * time.Second)
	j := &ent.RestoreJob{
		ID: uuid.New(), Status: restorejob.StatusRunning,
		CreatedAt: time.Now(), StartedAt: &start,
	}
	row := toRestoreFragRow(j)
	if row.Duration != "Running..." {
		t.Errorf("Duration = %q, want %q", row.Duration, "Running...")
	}
}

func TestToRestoreFragRow_BackupLabelFromKey(t *testing.T) {
	key := "backups/2025/06/01/backup-abc.tar.gz"
	j := &ent.RestoreJob{ID: uuid.New(), Status: restorejob.StatusCompleted, CreatedAt: time.Now(), BackupKey: &key}
	row := toRestoreFragRow(j)
	if row.BackupLabel != "backup-abc.tar.gz" {
		t.Errorf("BackupLabel = %q, want %q", row.BackupLabel, "backup-abc.tar.gz")
	}
}

func TestToRestoreFragRow_BackupLabelFromID(t *testing.T) {
	id := uuid.New()
	j := &ent.RestoreJob{ID: uuid.New(), Status: restorejob.StatusCompleted, CreatedAt: time.Now(), BackupID: &id}
	row := toRestoreFragRow(j)
	// 8 chars + "..." = 11
	if len(row.BackupLabel) != 11 {
		t.Errorf("BackupLabel = %q, unexpected length %d (want 11)", row.BackupLabel, len(row.BackupLabel))
	}
}

func TestToRestoreFragRow_HasLog(t *testing.T) {
	log := "some output"
	j := &ent.RestoreJob{ID: uuid.New(), Status: restorejob.StatusCompleted, CreatedAt: time.Now(), Log: &log}
	row := toRestoreFragRow(j)
	if !row.HasLog {
		t.Error("expected HasLog=true when Log is set")
	}
	if row.Log != log {
		t.Errorf("Log = %q, want %q", row.Log, log)
	}
}
