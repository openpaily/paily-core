package config

import (
	"fmt"
	"strings"

	"github.com/spf13/viper"
)

// CheckerEntry defines a named checker with its authentication secret.
type CheckerEntry struct {
	Tag           string `mapstructure:"tag"`
	Secret        string `mapstructure:"secret"`
	DefaultPreset string `mapstructure:"default_preset"` // e.g. "common" | "all" | "other"; empty = fall back to Config.DefaultPreset
}

// Config holds all runtime configuration loaded by viper.
type Config struct {
	Server        ServerConfig   `mapstructure:"server"`
	Database      DatabaseConfig `mapstructure:"database"`
	Auth          AuthConfig     `mapstructure:"auth"`
	Checkers      []CheckerEntry `mapstructure:"checkers"`
	DefaultPreset string         `mapstructure:"default_preset"` // global fallback preset; empty = "common"
}

type ServerConfig struct {
	Port        int      `mapstructure:"port"`
	CORSOrigins []string `mapstructure:"cors_origins"`
	Verbose     bool     `mapstructure:"verbose"` // log all requests when true; default false (errors/warnings only)
}

type DatabaseConfig struct {
	Type                   string `mapstructure:"type"` // "sqlite" | "postgres"
	DSN                    string `mapstructure:"dsn"`
	MaxOpenConns           int    `mapstructure:"max_open_conns"`
	MaxIdleConns           int    `mapstructure:"max_idle_conns"`
	ConnMaxLifetimeMinutes int    `mapstructure:"conn_max_lifetime_minutes"`
}

// AuthConfig holds secrets that must be provided via config file or env.
type AuthConfig struct {
	// AdminPassword is the plaintext admin password used during login.
	// Always load from env (PAILY_AUTH_ADMINPASSWORD) in production.
	AdminPassword string `mapstructure:"admin_password"`

	// ServiceSecret is the shared Bearer token for fetcher / checker services.
	ServiceSecret string `mapstructure:"service_secret"`

	// JWTSecret is the HMAC key for signing JWTs. Min 32 chars recommended.
	JWTSecret string `mapstructure:"jwt_secret"`
}

// Load reads config from file and environment variables.
// Precedence: env > config file > defaults.
func Load(cfgFile string) (*Config, error) {
	v := viper.New()

	// Environment variables: PAILY_SERVER_PORT, PAILY_DATABASE_DSN, etc.
	v.SetEnvPrefix("PAILY")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	// Config file
	if cfgFile != "" {
		v.SetConfigFile(cfgFile)
	} else {
		v.SetConfigName("config")
		v.SetConfigType("yaml")
		v.AddConfigPath(".")
		v.AddConfigPath("./config")
	}

	// Defaults
	v.SetDefault("server.port", 8080)
	v.SetDefault("database.type", "sqlite")
	v.SetDefault("database.dsn", "paily.db")
	v.SetDefault("database.max_open_conns", 0)
	v.SetDefault("database.max_idle_conns", 0)
	v.SetDefault("database.conn_max_lifetime_minutes", 0)
	// Auth defaults keep viper aware of these keys so AutomaticEnv works
	// during Unmarshal.
	v.SetDefault("auth.admin_password", "")
	v.SetDefault("auth.service_secret", "")
	v.SetDefault("auth.jwt_secret", "")

	if err := v.ReadInConfig(); err != nil {
		// A missing config file is only fatal when the path was specified
		// explicitly; otherwise fall back to defaults + env vars.
		if cfgFile != "" {
			return nil, fmt.Errorf("config: read %q: %w", cfgFile, err)
		}
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}
