package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	openapi_types "github.com/oapi-codegen/runtime/types"
	api "github.com/openpaily/paily-core/internal/api/generated"
	"github.com/openpaily/paily-core/internal/auth"
	"github.com/openpaily/paily-core/internal/cache"
	checkerPkg "github.com/openpaily/paily-core/internal/checker"
	"github.com/openpaily/paily-core/internal/config"
	"github.com/openpaily/paily-core/internal/distribution"
	fetcherPkg "github.com/openpaily/paily-core/internal/fetcher"
	nodePkg "github.com/openpaily/paily-core/internal/node"
	sourcePkg "github.com/openpaily/paily-core/internal/source"
	sponsorPkg "github.com/openpaily/paily-core/internal/sponsor"
	"gorm.io/gorm"
)

// Server implements api.ServerInterface.
type Server struct {
	auth         *auth.Handler
	sources      *SourceHandler
	fetcher      *fetcherPkg.Handler
	nodes        *NodeHandler
	checker      *checkerPkg.Handler
	sponsor      *SponsorHandler
	filter       *FilterHandler
	search       *SearchHandler
	formatConfig *FormatConfigHandler
	distribute   *distribution.Handler
	config       *ConfigHandler
	logs         *LogHandler
	stats        *StatsHandler
	cfg          *config.Config
}

// New creates a Server with its HTTP handlers and dependencies.
func New(
	adminPassword, jwtSecret string,
	sourceSvc *sourcePkg.Service,
	fetcherHandler *fetcherPkg.Handler,
	nodeSvc *nodePkg.Service,
	checkerHandler *checkerPkg.Handler,
	sponsorSvc *sponsorPkg.Service,
	aliveCache *cache.AliveCache,
	db *gorm.DB,
	cfg *config.Config,
) *Server {
	return &Server{
		auth:         auth.NewHandler(adminPassword, jwtSecret),
		sources:      &SourceHandler{svc: sourceSvc},
		fetcher:      fetcherHandler,
		nodes:        &NodeHandler{svc: nodeSvc, db: db},
		checker:      checkerHandler,
		sponsor:      &SponsorHandler{svc: sponsorSvc},
		filter:       &FilterHandler{db: db},
		search:       &SearchHandler{db: db},
		formatConfig: &FormatConfigHandler{db: db},
		distribute:   distribution.NewHandler(aliveCache, db, cfg),
		config:       &ConfigHandler{db: db},
		logs:         &LogHandler{db: db},
		stats:        &StatsHandler{db: db},
		cfg:          cfg,
	}
}

// ── Checkers ─────────────────────────────────────────────────────────────────

// CheckerList handles GET /api/v1/checkers.
func (s *Server) CheckerList(c *gin.Context) {
	tags := make([]string, 0, len(s.cfg.Checkers))
	for _, e := range s.cfg.Checkers {
		if e.Tag != "" {
			tags = append(tags, e.Tag)
		}
	}
	c.JSON(http.StatusOK, api.CheckerListResponse{Checkers: tags})
}

// ── Auth ──────────────────────────────────────────────────────────────────────

func (s *Server) AuthLogin(c *gin.Context)   { s.auth.Login(c) }
func (s *Server) AuthRefresh(c *gin.Context) { s.auth.Refresh(c) }

// ── Checker ───────────────────────────────────────────────────────────────────

func (s *Server) CheckGetNodes(c *gin.Context)    { s.checker.GetNodes(c) }
func (s *Server) CheckPostResults(c *gin.Context) { s.checker.PostResults(c) }

// ── Config ────────────────────────────────────────────────────────────────────

func (s *Server) ConfigGet(c *gin.Context)    { s.config.Get(c) }
func (s *Server) ConfigUpdate(c *gin.Context) { s.config.Update(c) }

// ── Fetch ─────────────────────────────────────────────────────────────────────

func (s *Server) FetchGetSources(c *gin.Context)  { s.fetcher.GetSources(c) }
func (s *Server) FetchPostResults(c *gin.Context) { s.fetcher.PostResults(c) }

// ── Filter ────────────────────────────────────────────────────────────────────

func (s *Server) FilterPreview(c *gin.Context) { s.filter.Preview(c) }

// ── Format configs ────────────────────────────────────────────────────────────

func (s *Server) FormatConfigList(c *gin.Context)                  { s.formatConfig.List(c) }
func (s *Server) FormatConfigGet(c *gin.Context, format string)    { s.formatConfig.Get(c, format) }
func (s *Server) FormatConfigUpdate(c *gin.Context, format string) { s.formatConfig.Update(c, format) }

// ── Logs ──────────────────────────────────────────────────────────────────────

func (s *Server) LogFetchList(c *gin.Context, p api.LogFetchListParams) { s.logs.FetchList(c, p) }
func (s *Server) LogCheckList(c *gin.Context, p api.LogCheckListParams) { s.logs.CheckList(c, p) }

// ── Nodes ─────────────────────────────────────────────────────────────────────

func (s *Server) NodeList(c *gin.Context, p api.NodeListParams)    { s.nodes.List(c, p) }
func (s *Server) NodeSearch(c *gin.Context)                        { s.search.NodeSearch(c) }
func (s *Server) NodeGet(c *gin.Context, id openapi_types.UUID)    { s.nodes.Get(c, id) }
func (s *Server) NodeDelete(c *gin.Context, id openapi_types.UUID) { s.nodes.Delete(c, id) }
func (s *Server) NodeGetInitialChecks(c *gin.Context, id openapi_types.UUID, p api.NodeGetInitialChecksParams) {
	s.nodes.GetInitialChecks(c, id, p)
}
func (s *Server) NodeGetDeepChecks(c *gin.Context, id openapi_types.UUID, p api.NodeGetDeepChecksParams) {
	s.nodes.GetDeepChecks(c, id, p)
}

// ── Sources ───────────────────────────────────────────────────────────────────

func (s *Server) SourceCreate(c *gin.Context)                        { s.sources.Create(c) }
func (s *Server) SourceList(c *gin.Context, p api.SourceListParams)  { s.sources.List(c, p) }
func (s *Server) SourceGet(c *gin.Context, id openapi_types.UUID)    { s.sources.Get(c, id) }
func (s *Server) SourceUpdate(c *gin.Context, id openapi_types.UUID) { s.sources.Update(c, id) }
func (s *Server) SourceDelete(c *gin.Context, id openapi_types.UUID) { s.sources.Delete(c, id) }
func (s *Server) SourceIgnoreDead(c *gin.Context, id openapi_types.UUID) {
	s.sources.IgnoreDead(c, id)
}

func (s *Server) SourceSearch(c *gin.Context) { s.search.SourceSearch(c) }
func (s *Server) SourceGetNodes(c *gin.Context, id openapi_types.UUID, p api.SourceGetNodesParams) {
	s.search.SourceGetNodes(c, id, p)
}

// ── Sponsors ──────────────────────────────────────────────────────────────────

func (s *Server) SponsorList(c *gin.Context, p api.SponsorListParams) { s.sponsor.List(c, p) }
func (s *Server) SponsorCreate(c *gin.Context)                        { s.sponsor.Create(c) }
func (s *Server) SponsorGet(c *gin.Context, id openapi_types.UUID)    { s.sponsor.Get(c, id) }
func (s *Server) SponsorUpdate(c *gin.Context, id openapi_types.UUID) { s.sponsor.Update(c, id) }
func (s *Server) SponsorDelete(c *gin.Context, id openapi_types.UUID) { s.sponsor.Delete(c, id) }

// ── Stats ─────────────────────────────────────────────────────────────────────

func (s *Server) StatsGet(c *gin.Context) { s.stats.Get(c) }

// ── Distribution ──────────────────────────────────────────────────────────────

func (s *Server) Distribute(c *gin.Context, f api.DistributeParamsFormat, p api.DistributeParams) {
	s.distribute.Distribute(c, f, p)
}
