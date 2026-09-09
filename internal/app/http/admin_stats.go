package http

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"fri.local/football-reputation-index/internal/domain"
	"fri.local/football-reputation-index/internal/service"
	"github.com/gin-gonic/gin"
)

// TrafficService is the analytics surface. Reached by type assertion rather
// than added to Service so the router's fakes stay small — a fake that does
// not implement it simply has no dashboard, which is also what a deployment
// without a real repository gets.
type TrafficService interface {
	RecordView(section, visitorHash string)
	TrafficStats(ctx context.Context) (*domain.TrafficStats, error)
}

// trafficSection maps a request path to the part of the site it belongs to,
// or "" for requests that should not be counted.
//
// Assets are skipped: a page load already counts once, and counting the
// logo and the script again would turn one visit into four. Health checks
// are skipped because they are ours, not anyone's visit.
//
// Everything the History-API router serves — /, /leaderboard, /vote,
// /about, /admin — is one document from the server, so these names are
// where a visitor ARRIVED. Moving between pages afterwards happens in the
// browser and never reaches us; the dashboard says so rather than passing
// entry points off as page views.
func trafficSection(path string) string {
	switch {
	case strings.HasPrefix(path, "/assets/"):
		return ""
	case path == "/api/health":
		return ""
	case strings.HasPrefix(path, "/api/"):
		return "api"
	case path == "/" || path == "":
		return "home"
	case path == "/leaderboard", path == "/vote", path == "/about", path == "/admin":
		return strings.TrimPrefix(path, "/")
	default:
		// Anything else still reached us and is still a visit — bots probing
		// /wp-login.php included. Bucketed rather than named, so a scanner
		// cannot fill the table with a row per invented path.
		return "other"
	}
}

// recordTraffic counts every served request. It runs after the handler so a
// counter can never delay or fail a response, and it holds nothing but a
// mutex on an in-memory map.
func (h *Router) recordTraffic(c *gin.Context) {
	c.Next()

	tracker, ok := h.svc.(TrafficService)
	if !ok {
		return
	}
	section := trafficSection(c.Request.URL.Path)
	if section == "" {
		return
	}
	// Only page loads identify a visitor. Counting the API calls a page
	// makes would not change the daily unique count — same address, same
	// day, one row — but doing the hashing once per visit rather than once
	// per request keeps it honest about what it measures.
	visitor := ""
	if section != "api" {
		visitor = service.TrafficVisitorHash(clientAddress(c), time.Now().UTC())
	}
	tracker.RecordView(section, visitor)
}

// clientAddress digs the caller's address out from behind Caddy. Gin
// resolves X-Forwarded-For itself, but the header is checked first so a
// misconfigured trusted-proxy list cannot quietly turn every visitor into
// the proxy's own address — which would report one unique visitor a day
// forever.
func clientAddress(c *gin.Context) string {
	if forwarded := strings.TrimSpace(c.GetHeader("X-Forwarded-For")); forwarded != "" {
		// Left-most entry is the original client; the rest are proxies.
		if first := strings.TrimSpace(strings.Split(forwarded, ",")[0]); first != "" {
			return first
		}
	}
	if real := strings.TrimSpace(c.GetHeader("X-Real-IP")); real != "" {
		return real
	}
	return c.ClientIP()
}

// adminStats serves the dashboard behind the admin gate. Everything on it —
// account numbers, traffic, what the syncs are doing — is business
// information, so it sits with the sync triggers rather than in public.
func (h *Router) adminStats(c *gin.Context) {
	tracker, ok := h.svc.(TrafficService)
	if !ok {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "statistics unavailable"})
		return
	}
	stats, err := tracker.TrafficStats(c.Request.Context())
	if errors.Is(err, service.ErrTrafficUnavailable) {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "statistics unavailable"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not read statistics"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": stats})
}
