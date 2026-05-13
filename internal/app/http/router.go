package http

import (
	"context"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"fri.local/football-reputation-index/internal/app/config"
	"fri.local/football-reputation-index/internal/domain"
	"github.com/gin-gonic/gin"
)

// Service is the surface used by HTTP handlers. Declared as an interface so
// router tests can swap in a fake without touching the real Service / DB.
// *service.Service satisfies this interface naturally.
type Service interface {
	ListPlayers(ctx context.Context, search, position, club string) ([]domain.PlayerWithScore, error)
	GetPlayer(ctx context.Context, id int64) (*domain.PlayerWithScore, error)
	GetHistory(ctx context.Context, playerID int64) ([]domain.HistoryPoint, error)
	ListNews(ctx context.Context, playerID *int64) ([]domain.NewsItem, error)
	SubmitVote(ctx context.Context, playerID int64, input domain.VoteInput, rawIP string) (*domain.Score, error)
	ListComponentUpdates(ctx context.Context, limit int) ([]domain.ComponentUpdate, error)
	SyncCareerBaseline(ctx context.Context) (*domain.ComponentSyncResult, error)
	SyncMedia(ctx context.Context) (*domain.ComponentSyncResult, error)
	SyncSocial(ctx context.Context) (*domain.ComponentSyncResult, error)
	SyncPerformance(ctx context.Context) (*domain.ComponentSyncResult, error)
	SyncCharacter(ctx context.Context) (*domain.ComponentSyncResult, error)
	SyncAll(ctx context.Context) ([]domain.ComponentSyncResult, error)
	// Phase 5: per-event fan voting
	ListPendingEvents(ctx context.Context, playerID int64, limit int) ([]domain.PendingEvent, error)
	GetPendingEvent(ctx context.Context, eventID int64) (*domain.PendingEvent, error)
	SubmitEventVote(ctx context.Context, eventID int64, suggestedDelta float64, rawIP string) error
	FinalizePendingEvents(ctx context.Context) (*domain.ComponentSyncResult, error)
}

type Router struct {
	cfg config.Config
	svc Service
}

func NewRouter(cfg config.Config, svc Service) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	router := gin.Default()

	handler := &Router{
		cfg: cfg,
		svc: svc,
	}

	api := router.Group("/api")
	{
		api.GET("/health", handler.health)
		api.GET("/players", handler.listPlayers)
		api.GET("/players/:id", handler.getPlayer)
		api.GET("/players/:id/history", handler.getPlayerHistory)
		api.GET("/players/:id/news", handler.getPlayerNews)
		api.POST("/players/:id/vote", handler.submitVote) // legacy — kept for compat, see handler
		api.GET("/leaderboard", handler.listPlayers)
		api.GET("/news/feed", handler.listNewsFeed)
		api.GET("/sync/updates", handler.listComponentUpdates)
		// Phase 5: per-event fan voting
		api.GET("/events/pending", handler.listPendingEvents)
		api.GET("/events/:id", handler.getPendingEvent)
		api.POST("/events/:id/vote", handler.submitEventVote)
		api.POST("/sync/finalize-events", handler.runFinalizeEvents)
		api.POST("/sync/career-baseline", handler.runCareerBaselineSync)
		api.POST("/sync/media", handler.runMediaSync)
		api.POST("/sync/social", handler.runSocialSync)
		api.POST("/sync/performance", handler.runPerformanceSync)
		api.POST("/sync/character", handler.runCharacterSync)
		api.POST("/sync/all", handler.runAllSync)
	}

	router.Static("/assets", filepath.Join(cfg.WebDir, "assets"))
	router.NoRoute(func(c *gin.Context) {
		c.File(filepath.Join(cfg.WebDir, "index.html"))
	})

	return router
}

func (r *Router) health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (r *Router) listPlayers(c *gin.Context) {
	players, err := r.svc.ListPlayers(c.Request.Context(), c.Query("search"), c.Query("position"), c.Query("club"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"data": players})
}

func (r *Router) getPlayer(c *gin.Context) {
	playerID, ok := parseID(c)
	if !ok {
		return
	}

	player, err := r.svc.GetPlayer(c.Request.Context(), playerID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "player not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"data": player})
}

func (r *Router) getPlayerHistory(c *gin.Context) {
	playerID, ok := parseID(c)
	if !ok {
		return
	}

	points, err := r.svc.GetHistory(c.Request.Context(), playerID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"data": points})
}

func (r *Router) getPlayerNews(c *gin.Context) {
	playerID, ok := parseID(c)
	if !ok {
		return
	}

	items, err := r.svc.ListNews(c.Request.Context(), &playerID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"data": items})
}

func (r *Router) listNewsFeed(c *gin.Context) {
	items, err := r.svc.ListNews(c.Request.Context(), nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"data": items})
}

func (r *Router) submitVote(c *gin.Context) {
	playerID, ok := parseID(c)
	if !ok {
		return
	}

	var payload domain.VoteInput
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	score, err := r.svc.SubmitVote(c.Request.Context(), playerID, payload, c.ClientIP())
	if err != nil {
		// Rate-limit errors get 429 so the frontend can show a cooldown UI.
		if strings.Contains(err.Error(), "rate limit") {
			c.JSON(http.StatusTooManyRequests, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, gin.H{"data": score})
}

func (r *Router) listComponentUpdates(c *gin.Context) {
	updates, err := r.svc.ListComponentUpdates(c.Request.Context(), 20)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": updates})
}

func (r *Router) runCareerBaselineSync(c *gin.Context) {
	result, err := r.svc.SyncCareerBaseline(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error(), "data": result})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": result})
}

func (r *Router) runMediaSync(c *gin.Context) {
	result, err := r.svc.SyncMedia(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error(), "data": result})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": result})
}

func (r *Router) runSocialSync(c *gin.Context) {
	result, err := r.svc.SyncSocial(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error(), "data": result})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": result})
}

func (r *Router) runPerformanceSync(c *gin.Context) {
	result, err := r.svc.SyncPerformance(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error(), "data": result})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": result})
}

func (r *Router) runCharacterSync(c *gin.Context) {
	result, err := r.svc.SyncCharacter(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error(), "data": result})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": result})
}

func (r *Router) runAllSync(c *gin.Context) {
	results, err := r.svc.SyncAll(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error(), "data": results})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": results})
}

func parseID(c *gin.Context) (int64, bool) {
	playerID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid player id"})
		return 0, false
	}
	return playerID, true
}

// ────────────────────────────────────────────────────────────────────────
// Phase 5 — per-event fan voting endpoints
// ────────────────────────────────────────────────────────────────────────

// listPendingEvents serves the event voting queue. Optional ?player_id=X
// scopes to one player; otherwise returns the site-wide feed. ?limit=N
// caps the number of rows.
func (r *Router) listPendingEvents(c *gin.Context) {
	var playerID int64
	if raw := strings.TrimSpace(c.Query("player_id")); raw != "" {
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid player_id"})
			return
		}
		playerID = v
	}
	limit := 50
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 && v <= 200 {
			limit = v
		}
	}
	events, err := r.svc.ListPendingEvents(c.Request.Context(), playerID, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if events == nil {
		events = []domain.PendingEvent{}
	}
	c.JSON(http.StatusOK, gin.H{"data": events})
}

// getPendingEvent serves one event's vote state. Returns 404 if the event
// is finalized or doesn't exist — the UI shouldn't try to vote on it.
func (r *Router) getPendingEvent(c *gin.Context) {
	eventID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid event id"})
		return
	}
	event, err := r.svc.GetPendingEvent(c.Request.Context(), eventID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if event == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "event not pending"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": event})
}

// submitEventVote records one fan's slider value on an event. Returns:
//
//	201 — vote recorded (new or updated; UI doesn't distinguish)
//	400 — bad input
//	410 — event no longer accepting votes (finalized, closed, or unknown)
//	500 — server-side failure
//
// The handler reads X-Real-IP (set by Caddy in front of the app) and falls
// back to the connection IP; the service hashes it before storage.
func (r *Router) submitEventVote(c *gin.Context) {
	eventID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid event id"})
		return
	}
	var input domain.EventVoteInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "body must be JSON {\"suggested_delta\": <float>}"})
		return
	}

	rawIP := strings.TrimSpace(c.GetHeader("X-Real-IP"))
	if rawIP == "" {
		rawIP = c.ClientIP()
	}

	if err := r.svc.SubmitEventVote(c.Request.Context(), eventID, input.SuggestedDelta, rawIP); err != nil {
		// service.ErrEventGone maps to HTTP 410. We test via string-match to
		// avoid an import cycle with the service package — the error is
		// stable and exported as a sentinel value.
		if strings.Contains(err.Error(), "no longer accepting votes") {
			c.JSON(http.StatusGone, gin.H{"error": "event no longer accepts votes"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"data": gin.H{"event_id": eventID, "status": "recorded"}})
}

// runFinalizeEvents is the manual trigger for the hourly finalize cron.
// Useful for QA — the user can press a button to immediately apply pending
// events without waiting an hour.
func (r *Router) runFinalizeEvents(c *gin.Context) {
	result, err := r.svc.FinalizePendingEvents(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error(), "data": result})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": result})
}
