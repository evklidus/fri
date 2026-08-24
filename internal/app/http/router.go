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
		// Accounts. Registration exists so the product can report how many
		// people use it; the leaderboard gate and the admin tools are built
		// on the same sessions.
		api.POST("/auth/register", handler.register)
		api.POST("/auth/login", handler.login)
		api.POST("/auth/logout", handler.logout)
		api.GET("/auth/me", handler.me)
		api.GET("/stats/users", handler.userCount)

		// Everything that changes data sits behind an admin session. These
		// were open to the internet until now: anyone who knew the paths
		// could trigger a full sync (burning metered API quota) or, once
		// news deletion landed, empty the feed.
		admin := api.Group("", handler.requireAdmin)
		{
			admin.DELETE("/news/:id", handler.deleteNewsItem)
			admin.POST("/sync/finalize-events", handler.runFinalizeEvents)
			admin.POST("/sync/career-baseline", handler.runCareerBaselineSync)
			admin.POST("/sync/media", handler.runMediaSync)
			admin.POST("/sync/social", handler.runSocialSync)
			admin.POST("/sync/performance", handler.runPerformanceSync)
			admin.POST("/sync/character", handler.runCharacterSync)
			admin.POST("/sync/all", handler.runAllSync)
		}
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
	// The top places are the reason to create an account, so they are
	// withheld rather than merely blurred — see maskLockedPlayers.
	_, signedIn := r.currentUser(c)
	filtered := c.Query("search") != "" || c.Query("position") != "" || c.Query("club") != ""

	// Establish the gate before fetching what was asked for. The withheld set
	// must come from the unfiltered roster: deciding it from the filtered
	// slice is what turned the filters into an oracle.
	var locked map[int64]bool
	if !signedIn {
		roster := []domain.PlayerWithScore(nil)
		if filtered {
			var err error
			roster, err = r.svc.ListPlayers(c.Request.Context(), "", "", "")
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			if len(roster) == 0 {
				c.JSON(http.StatusServiceUnavailable, gin.H{"error": "temporarily unavailable"})
				return
			}
			locked = lockedPlayerIDs(roster)
		}
	}

	players, err := r.svc.ListPlayers(c.Request.Context(), c.Query("search"), c.Query("position"), c.Query("club"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if !signedIn {
		// Unfiltered request: the list we just fetched is the roster.
		if locked == nil {
			locked = lockedPlayerIDs(players)
		}
		players = maskLockedPlayers(players, locked, false, filtered)
	}

	c.JSON(http.StatusOK, gin.H{
		"data": players,
		"meta": gin.H{"locked_top": lockedTopN, "signed_in": signedIn},
	})
}

func (r *Router) getPlayer(c *gin.Context) {
	playerID, ok := parseID(c)
	if !ok {
		return
	}

	// A withheld player must not be reachable by guessing an id — there are
	// only 22 of them.
	locked, err := r.lockedForCaller(c, playerID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if locked {
		abortLocked(c)
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

	// A withheld player must not be reachable by guessing an id — there are
	// only 22 of them.
	locked, err := r.lockedForCaller(c, playerID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if locked {
		abortLocked(c)
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

	// A withheld player must not be reachable by guessing an id — there are
	// only 22 of them.
	locked, err := r.lockedForCaller(c, playerID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if locked {
		abortLocked(c)
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

	// The feed names players and links their photos, so leaving it open would
	// have handed an anonymous visitor the top five that the leaderboard
	// withholds. Resolving who is locked needs the ordered player list, which
	// is one extra query on a page that already loads it.
	_, signedIn := r.currentUser(c)
	if !signedIn {
		players, err := r.svc.ListPlayers(c.Request.Context(), "", "", "")
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if len(players) == 0 {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "temporarily unavailable"})
			return
		}
		items = maskLockedNews(items, lockedPlayerIDs(players), lockedPlayerNames(players), false)
	}

	c.JSON(http.StatusOK, gin.H{
		"data": items,
		"meta": gin.H{"signed_in": signedIn},
	})
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

	// A write must not double as a read. This endpoint returned the freshly
	// recomputed score, so one throwaway vote against a withheld player's id
	// handed back their full breakdown — the exact thing the gate withholds,
	// and the per-IP cooldown was no help because a single request sufficed.
	// It also let an anonymous caller nudge a hidden player's score.
	locked, err := r.lockedForCaller(c, playerID)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "temporarily unavailable"})
		return
	}
	if locked {
		abortLocked(c)
		return
	}

	if _, err := r.svc.SubmitVote(c.Request.Context(), playerID, payload, c.ClientIP()); err != nil {
		// Rate-limit errors get 429 so the frontend can show a cooldown UI.
		if strings.Contains(err.Error(), "rate limit") {
			c.JSON(http.StatusTooManyRequests, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Acknowledge without echoing the score back.
	c.JSON(http.StatusCreated, gin.H{"data": gin.H{"recorded": true}})
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
	// Events name the player they belong to, and their news_title is a
	// headline that names them again — an open events feed listed the top
	// five as clearly as the leaderboard would have.
	if _, signedIn := r.currentUser(c); !signedIn {
		players, listErr := r.svc.ListPlayers(c.Request.Context(), "", "", "")
		if listErr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": listErr.Error()})
			return
		}
		if len(players) == 0 {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "temporarily unavailable"})
			return
		}
		events = maskLockedEvents(events, lockedPlayerIDs(players), lockedPlayerNames(players), false)
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
	// Same gate as the list: fetching one event by id must not be a way
	// around it.
	locked, err := r.lockedForCaller(c, event.PlayerID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if locked {
		masked := maskLockedEvents([]domain.PendingEvent{*event}, map[int64]bool{event.PlayerID: true}, nil, false)
		c.JSON(http.StatusOK, gin.H{"data": masked[0]})
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
