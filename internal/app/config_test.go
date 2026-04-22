package app_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marcoantonios1/Agent-OS/internal/app"
)

// setEnv sets env vars for the duration of the test, restoring original values
// (or unsetting) when the test completes.
func setEnv(t *testing.T, pairs map[string]string) {
	t.Helper()
	for k, v := range pairs {
		prev, existed := os.LookupEnv(k)
		os.Setenv(k, v) //nolint:errcheck
		t.Cleanup(func() {
			if existed {
				os.Setenv(k, prev) //nolint:errcheck
			} else {
				os.Unsetenv(k) //nolint:errcheck
			}
		})
	}
}

// clearEnv ensures the given keys are unset for the duration of the test.
func clearEnv(t *testing.T, keys ...string) {
	t.Helper()
	for _, k := range keys {
		prev, existed := os.LookupEnv(k)
		os.Unsetenv(k) //nolint:errcheck
		t.Cleanup(func() {
			if existed {
				os.Setenv(k, prev) //nolint:errcheck
			}
		})
	}
}

func writeDotEnv(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write temp .env: %v", err)
	}
	return path
}

func TestLoad_MissingCostguardURL_ReturnsError(t *testing.T) {
	clearEnv(t, "COSTGUARD_URL")
	_, err := app.Load("nonexistent.env")
	if err == nil {
		t.Fatal("expected error when COSTGUARD_URL is missing, got nil")
	}
	if want := "COSTGUARD_URL is required"; !containsStr(err.Error(), want) {
		t.Errorf("error %q does not contain %q", err.Error(), want)
	}
}

func TestLoad_ValidConfig_Succeeds(t *testing.T) {
	setEnv(t, map[string]string{"COSTGUARD_URL": "http://host.docker.internal:8080"})
	cfg, err := app.Load("nonexistent.env")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.CostguardURL != "http://host.docker.internal:8080" {
		t.Errorf("CostguardURL = %q, want %q", cfg.CostguardURL, "http://host.docker.internal:8080")
	}
}

func TestLoad_Defaults(t *testing.T) {
	setEnv(t, map[string]string{"COSTGUARD_URL": "http://host.docker.internal:8080"})
	clearEnv(t, "PORT", "LOG_LEVEL", "BUILDER_SANDBOX_DIR", "SESSION_TTL")

	cfg, err := app.Load("nonexistent.env")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Port != "9091" {
		t.Errorf("Port = %q, want %q", cfg.Port, "9091")
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel = %q, want %q", cfg.LogLevel, "info")
	}
	if cfg.BuilderSandboxDir != "workspace" {
		t.Errorf("BuilderSandboxDir = %q, want %q", cfg.BuilderSandboxDir, "workspace")
	}
	if cfg.SessionTTL != 24*time.Hour {
		t.Errorf("SessionTTL = %v, want %v", cfg.SessionTTL, 24*time.Hour)
	}
}

func TestLoad_ModelDefaults(t *testing.T) {
	setEnv(t, map[string]string{"COSTGUARD_URL": "http://host.docker.internal:8080"})
	clearEnv(t, "COMMS_MODEL", "BUILDER_MODEL", "RESEARCH_MODEL", "CLASSIFIER_MODEL")

	cfg, err := app.Load("nonexistent.env")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	const want = "gemma4:26b"
	if cfg.CommsModel != want {
		t.Errorf("CommsModel = %q, want %q", cfg.CommsModel, want)
	}
	if cfg.BuilderModel != want {
		t.Errorf("BuilderModel = %q, want %q", cfg.BuilderModel, want)
	}
	if cfg.ResearchModel != want {
		t.Errorf("ResearchModel = %q, want %q", cfg.ResearchModel, want)
	}
	if cfg.ClassifierModel != want {
		t.Errorf("ClassifierModel = %q, want %q", cfg.ClassifierModel, want)
	}
}

func TestLoad_ModelEnvOverride(t *testing.T) {
	setEnv(t, map[string]string{
		"COSTGUARD_URL":    "http://host.docker.internal:8080",
		"COMMS_MODEL":      "claude-sonnet-4-6",
		"BUILDER_MODEL":    "claude-opus-4-7",
		"RESEARCH_MODEL":   "claude-haiku-4-5-20251001",
		"CLASSIFIER_MODEL": "claude-sonnet-4-6",
	})

	cfg, err := app.Load("nonexistent.env")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.CommsModel != "claude-sonnet-4-6" {
		t.Errorf("CommsModel = %q, want %q", cfg.CommsModel, "claude-sonnet-4-6")
	}
	if cfg.BuilderModel != "claude-opus-4-7" {
		t.Errorf("BuilderModel = %q, want %q", cfg.BuilderModel, "claude-opus-4-7")
	}
	if cfg.ResearchModel != "claude-haiku-4-5-20251001" {
		t.Errorf("ResearchModel = %q, want %q", cfg.ResearchModel, "claude-haiku-4-5-20251001")
	}
	if cfg.ClassifierModel != "claude-sonnet-4-6" {
		t.Errorf("ClassifierModel = %q, want %q", cfg.ClassifierModel, "claude-sonnet-4-6")
	}
}

func TestLoad_DotEnvFile_SetsValues(t *testing.T) {
	clearEnv(t, "COSTGUARD_URL", "PORT", "LOG_LEVEL")

	path := writeDotEnv(t, `
# comment line
COSTGUARD_URL=http://costguard:9000
PORT=8888
LOG_LEVEL=debug
`)
	cfg, err := app.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.CostguardURL != "http://costguard:9000" {
		t.Errorf("CostguardURL = %q, want %q", cfg.CostguardURL, "http://costguard:9000")
	}
	if cfg.Port != "8888" {
		t.Errorf("Port = %q, want %q", cfg.Port, "8888")
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel = %q, want %q", cfg.LogLevel, "debug")
	}
}

func TestLoad_EnvVarOverridesDotEnv(t *testing.T) {
	setEnv(t, map[string]string{"COSTGUARD_URL": "http://real:8080"})

	path := writeDotEnv(t, "COSTGUARD_URL=http://from-file:9999\n")
	cfg, err := app.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Real env var must win.
	if cfg.CostguardURL != "http://real:8080" {
		t.Errorf("CostguardURL = %q, want %q (real env var should override .env)", cfg.CostguardURL, "http://real:8080")
	}
}

func TestLoad_DotEnvFile_Quoted(t *testing.T) {
	clearEnv(t, "COSTGUARD_URL", "COSTGUARD_API_KEY")

	path := writeDotEnv(t, `COSTGUARD_URL="http://quoted:8080"
COSTGUARD_API_KEY='my-key'
`)
	cfg, err := app.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.CostguardURL != "http://quoted:8080" {
		t.Errorf("CostguardURL = %q, want %q", cfg.CostguardURL, "http://quoted:8080")
	}
	if cfg.CostguardAPIKey != "my-key" {
		t.Errorf("CostguardAPIKey = %q, want %q", cfg.CostguardAPIKey, "my-key")
	}
}

func TestLoad_MissingDotEnvFile_NoError(t *testing.T) {
	setEnv(t, map[string]string{"COSTGUARD_URL": "http://host.docker.internal:8080"})
	_, err := app.Load("this-file-does-not-exist.env")
	if err != nil {
		t.Errorf("missing .env file should not be an error, got: %v", err)
	}
}

func TestLoad_SessionTTL_Parsed(t *testing.T) {
	setEnv(t, map[string]string{
		"COSTGUARD_URL": "http://host.docker.internal:8080",
		"SESSION_TTL":   "2h30m",
	})
	cfg, err := app.Load("nonexistent.env")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := 2*time.Hour + 30*time.Minute
	if cfg.SessionTTL != want {
		t.Errorf("SessionTTL = %v, want %v", cfg.SessionTTL, want)
	}
}

func TestConfig_ProviderHelpers(t *testing.T) {
	base := &app.Config{} // all empty

	if base.GoogleConfigured() {
		t.Error("GoogleConfigured() should be false when credentials are missing")
	}
	if base.MicrosoftConfigured() {
		t.Error("MicrosoftConfigured() should be false when credentials are missing")
	}

	full := &app.Config{
		GoogleClientID:        "id",
		GoogleClientSecret:    "secret",
		GoogleRefreshToken:    "token",
		MicrosoftClientID:     "id",
		MicrosoftRefreshToken: "token",
	}
	if !full.GoogleConfigured() {
		t.Error("GoogleConfigured() should be true when all credentials are set")
	}
	if !full.MicrosoftConfigured() {
		t.Error("MicrosoftConfigured() should be true when credentials are set")
	}
}

func containsStr(s, substr string) bool {
	return strings.Contains(s, substr)
}
