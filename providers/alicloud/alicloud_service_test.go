package alicloud

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigFromProfileUsesHomeDirOnEveryOS(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)        // read by os.UserHomeDir on Unix
	t.Setenv("USERPROFILE", home) // read by os.UserHomeDir on Windows
	config := `{"current": "dev", "profiles": [
		{"name": "default", "region_id": "cn-hangzhou"},
		{"name": "dev", "region_id": "eu-central-1"}
	]}`
	if err := os.MkdirAll(filepath.Join(home, ".aliyun"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".aliyun", "config.json"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	conf, err := LoadConfigFromProfile("")
	if err != nil {
		t.Fatal(err)
	}
	if conf.RegionID != "eu-central-1" {
		t.Errorf("want the current profile's region eu-central-1, got %q", conf.RegionID)
	}
}
