package config

// NativeManagementConfig configures the optional, in-process management modules.
// Startup settings require a restart; individual modules can also be switched in the UI.
type NativeManagementConfig struct {
	Enabled       bool            `yaml:"enabled" json:"enabled"`
	Database      string          `yaml:"database" json:"database"`
	RetentionDays int             `yaml:"retention-days" json:"retention-days"`
	Modules       map[string]bool `yaml:"modules" json:"modules"`
}
