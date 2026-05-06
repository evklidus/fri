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
	SyncMedia(ctx context.Context) (*domain.ComponentSyncResult, error)
	SyncSocial(ctx context.Context) (*domain.ComponentSyncResult, error)
	SyncPerformance(ctx context.Context) (*domain.ComponentSyncResult, error)
	SyncCharacter(ctx context.Context) (*domain.ComponentSyncResult, error)
	SyncAll(ctx context.Context) ([]domain.ComponentSyncResult, error)
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
		api.POST("/players/:id/vote", handler.submitVote)
		api.GET("/leaderboard", handler.listPlayers)
		api.GET("/news/feed", handler.listNewsFeed)
		api.GET("/sync/updates", handler.listComponentUpdates)
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
