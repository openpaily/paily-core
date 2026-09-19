package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	api "github.com/openpaily/paily-core/internal/api/generated"
	"github.com/openpaily/paily-core/internal/api/handler"
	"github.com/openpaily/paily-core/internal/api/middleware"
	"github.com/openpaily/paily-core/internal/auth"
	"github.com/openpaily/paily-core/internal/cache"
	"github.com/openpaily/paily-core/internal/checker"
	"github.com/openpaily/paily-core/internal/cleanup"
	"github.com/openpaily/paily-core/internal/config"
	"github.com/openpaily/paily-core/internal/db"
	"github.com/openpaily/paily-core/internal/fetcher"
	"github.com/openpaily/paily-core/internal/migrate"
	"github.com/openpaily/paily-core/internal/node"
	"github.com/openpaily/paily-core/internal/scoring"
	"github.com/openpaily/paily-core/internal/source"
	"github.com/openpaily/paily-core/internal/sponsor"
	"github.com/openpaily/paily-core/internal/templates"
	"github.com/openpaily/paily-fetch/format"

	// Trigger format plugin registration via init().
	_ "github.com/openpaily/paily-fetch/format/base64"
	_ "github.com/openpaily/paily-fetch/format/clash"
	_ "github.com/openpaily/paily-fetch/format/singbox"
)

func main() {
	cfgFile := flag.String("config", "", "path to config file (default: ./config.yaml)")
	migrateFlag := flag.Bool("migrate", false, "migrate config tables from SQLite to PostgreSQL, then exit")
	migrateFrom := flag.String("migrate-from", "", "path to source SQLite database file (required with -migrate)")
	flag.Parse()
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr}).Level(zerolog.InfoLevel)

	// ── Migration mode ────────────────────────────────────────────────────────
	if *migrateFlag {
		if *migrateFrom == "" {
			log.Fatal().Msg("-migrate-from is required when using -migrate")
		}
		cfg, err := config.Load(*cfgFile)
		if err != nil {
			log.Fatal().Err(err).Msg("failed to load config")
		}
		if cfg.Database.Type != "postgres" {
			log.Fatal().Msg("target database must be type=postgres in config")
		}
		if err := migrate.Run(*migrateFrom, cfg.Database.DSN); err != nil {
			log.Fatal().Err(err).Msg("migration failed")
		}
		return
	}

	cfg, err := config.Load(*cfgFile)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to load config")
	}

	database, err := db.Init(cfg)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to init database")
	}
	sqlDB, _ := database.DB()
	defer sqlDB.Close()

	// ── Dependency wiring ────────────────────────────────────────────────────
	nodeSvc := node.NewService(database)
	upsertSvc := node.NewUpsertService(database)
	sourceSvc := source.NewService(database, nodeSvc)
	fetchHandler := fetcher.NewHandler(sourceSvc, upsertSvc)
	scoringSvc := scoring.NewService(database)
	aliveCache := cache.New(database, cfg)
	checkerHandler := checker.NewHandler(database, sourceSvc, nodeSvc, scoringSvc, aliveCache)

	// Start periodic history cleanup goroutine.
	cleanup.Start(context.Background(), database)

	sponsorSvc := sponsor.NewService(database)

	// Seed default format configs for every registered format. A format with a
	// built-in template uses the embedded one; otherwise the factory default is
	// used. Customized configs are preserved; never-customized ones are upgraded.
	for _, f := range format.All() {
		defaultCfg := []byte(f.DefaultConfig())
		if embedded, ok := templates.Default(f.Name()); ok {
			defaultCfg = embedded
		}
		db.SeedFormatConfig(database, f.Name(), defaultCfg)
	}

	// Trigger an initial cache rebuild so distribution is ready immediately on startup.
	aliveCache.TriggerRebuild(context.Background())

	// ── HTTP engine ──────────────────────────────────────────────────────────
	gin.SetMode(gin.ReleaseMode)
	engine := gin.New()
	engine.Use(middleware.Logger(cfg.Server.Verbose))
	engine.Use(middleware.Recovery())
	engine.Use(corsMiddleware(cfg.Server.CORSOrigins))

	// Service credentials are restricted to the machine-to-machine endpoints.
	// All other API routes require an administrator JWT.
	svcMW := auth.ServiceBearerMiddleware(cfg.Auth.ServiceSecret)
	adminMW := auth.AdminJWTMiddleware(cfg.Auth.JWTSecret)
	sourceCreateMW := auth.AdminOrServiceMiddleware(cfg.Auth.JWTSecret, cfg.Auth.ServiceSecret)

	engine.Use(func(c *gin.Context) {
		p := c.FullPath()
		m := c.Request.Method
		switch {
		case p == "" ||
			p == "/:format" ||
			p == "/health" ||
			strings.HasPrefix(p, "/api/v1/auth/"):
			// public — no auth required
		case strings.HasPrefix(p, "/api/v1/check/"):
			// checker-specific: authenticates via checker secret and injects checker_tag
			middleware.CheckerTagMiddleware(cfg)(c)
		case strings.HasPrefix(p, "/api/v1/fetch/"):
			// service-facing endpoints
			svcMW(c)
		case p == "/api/v1/sources" && m == http.MethodPost:
			// Source creation is shared by the administrator UI and Catcher clients.
			sourceCreateMW(c)
		default:
			adminMW(c)
		}
		if !c.IsAborted() {
			c.Next()
		}
	})

	srv := handler.New(cfg.Auth.AdminPassword, cfg.Auth.JWTSecret, sourceSvc, fetchHandler, nodeSvc, checkerHandler, sponsorSvc, aliveCache, database, cfg)
	api.RegisterHandlers(engine, srv)

	// Health probe — always returns 200, no auth required.
	engine.GET("/health", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	addr := fmt.Sprintf(":%d", cfg.Server.Port)
	log.Info().Str("addr", addr).Msg("paily-core starting")

	if err := engine.Run(addr); err != nil {
		log.Fatal().Err(err).Msg("server exited")
	}
}

func corsMiddleware(allowedOrigins []string) gin.HandlerFunc {
	originSet := make(map[string]struct{}, len(allowedOrigins))
	for _, o := range allowedOrigins {
		o = strings.TrimRight(o, "/")
		if o != "" && o != "*" {
			originSet[o] = struct{}{}
		}
	}

	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if origin == "" {
			c.Next()
			return
		}

		_, allowed := originSet[strings.TrimRight(origin, "/")]

		if allowed {
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Api-Key, X-Request-ID")
			c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		}

		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}
