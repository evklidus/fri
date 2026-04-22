package http

import (
	"net/http"
	"path/filepath"
	"strconv"

	"fri.local/football-reputation-index/internal/app/config"
	"fri.local/football-reputation-index/internal/domain"
	"fri.local/football-reputation-index/internal/service"
	"github.com/gin-gonic/gin"
)

type Router struct {
	cfg config.Config
	svc *service.Service
}

func NewRouter(cfg config.Config, svc *service.Service) *gin.Engine {
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
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, gin.H{"data": score})
}

func parseID(c *gin.Context) (int64, bool) {
	playerID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid player id"})
		return 0, false
	}
	return playerID, true
}
