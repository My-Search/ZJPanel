package plugin

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Manifest plugin.yaml 结构。
type Manifest struct {
	ID          string   `yaml:"id" json:"id"`
	Name        string   `yaml:"name" json:"name"`
	Version     string   `yaml:"version" json:"version"`
	Description string   `yaml:"description" json:"description"`
	Permissions []string `yaml:"permissions" json:"permissions"`
	Menu        []struct {
		Title string `yaml:"title" json:"title"`
		Icon  string `yaml:"icon" json:"icon"`
		Path  string `yaml:"path" json:"path"`
	} `yaml:"menu" json:"menu"`
}

func parseManifestBytes(b []byte) (*Manifest, error) {
	var mf Manifest
	if err := yaml.Unmarshal(b, &mf); err != nil {
		return nil, fmt.Errorf("plugin.yaml 解析失败: %w", err)
	}
	if mf.ID == "" || !nameRe.MatchString(mf.ID) {
		return nil, fmt.Errorf("plugin.yaml id 非法: %q", mf.ID)
	}
	return &mf, nil
}

func loadManifestDir(dir string) (*Manifest, error) {
	b, err := os.ReadFile(filepath.Join(dir, "plugin.yaml"))
	if err != nil {
		return nil, fmt.Errorf("缺少 plugin.yaml")
	}
	return parseManifestBytes(b)
}
