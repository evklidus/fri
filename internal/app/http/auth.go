package http

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"fri.local/football-reputation-index/internal/domain"
	"fri.local/football-reputation-index/internal/service"
)

// sessionCookieName is the browser cookie holding an opaque session token.
const sessionCookieName = "fri_session"

// AuthService is the account surface the HTTP layer needs. Split from the
// main Service interface so router tests that don't care about accounts can
// keep using a fake without implementing any of this.
type AuthService interface {
	AuthEnabled() bool
	Register(ctx context.Context, email, password string) (domain.User, string, time.Time, error)
	Login(ctx context.Context, email, password string) (domain.User, string, time.Time, error)
	Logout(ctx context.Context, token string) error
	UserBySession(ctx context.Context, token string) (domain.User, error)
	CountUsers(ctx context.Context) (int64, error)
}

// authService returns the account surface when the wired service implements
// it and has a store behind it.
func (h *Router) authService() (AuthService, bool) {
	auth, ok := h.svc.(AuthService)
	if !ok || !auth.AuthEnabled() {
		return nil, false
	}
	return auth, true
}

// setSessionCookie writes the session token.
//
// HttpOnly keeps it away from JavaScript, so an XSS bug can't read it out.
// SameSite=Lax stops another site from riding the cookie on a cross-site POST
// while still letting normal top-level navigation into the site stay logged
// in. Secure is set whenever the request arrived over TLS — in production it
// always does, and leaving it off for plain HTTP keeps local development on
// http://localhost working.
func setSessionCookie(c *gin.Context, token string, expiresAt time.Time) {
	maxAge := int(time.Until(expiresAt).Seconds())
	if maxAge < 0 {
		maxAge = 0
	}
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   c.Request.TLS != nil || strings.EqualFold(c.GetHeader("X-Forwarded-Proto"), "https"),
		SameSite: http.SameSiteLaxMode,
	})
}

func clearSessionCookie(c *gin.Context) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// currentUser resolves the session cookie. The second return distinguishes
// "signed in" from "anonymous"; anonymous is a normal state here, not an
// error, because most of the site is readable without an account.
func (h *Router) currentUser(c *gin.Context) (domain.User, bool) {
	auth, ok := h.authService()
	if !ok {
		return domain.User{}, false
	}
	token, err := c.Cookie(sessionCookieName)
	if err != nil || strings.TrimSpace(token) == "" {
		return domain.User{}, false
	}
	user, err := auth.UserBySession(c.Request.Context(), token)
	if err != nil {
		return domain.User{}, false
	}
	return user, true
}

// adminTokenHeader lets machines authenticate without a browser session.
const adminTokenHeader = "X-Admin-Token"

// requireAdmin guards routes that change data. Anything destructive — the
// manual news deletion, the sync triggers — sits behind this.
//
// Two ways in. A browser presents an admin session cookie. A script presents
// ADMIN_API_TOKEN in a header, because deploy tooling and cron have no
// browser and the sync endpoints are operational tooling as much as UI. The
// token path is disabled when the variable is unset, so an unconfigured
// deployment doesn't silently accept an empty header.
func (h *Router) requireAdmin(c *gin.Context) {
	if expected := strings.TrimSpace(h.cfg.AdminAPIToken); expected != "" {
		if presented := strings.TrimSpace(c.GetHeader(adminTokenHeader)); presented != "" {
			// Constant-time compare: a byte-by-byte early exit leaks the
			// token's prefix to anyone willing to time enough requests.
			if subtle.ConstantTimeCompare([]byte(presented), []byte(expected)) == 1 {
				c.Next()
				return
			}
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "invalid admin token"})
			return
		}
	}

	user, ok := h.currentUser(c)
	if !ok {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "sign in required"})
		return
	}
	if !user.IsAdmin {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "admin only"})
		return
	}
	c.Set("user", user)
	c.Next()
}

func (h *Router) register(c *gin.Context) {
	auth, ok := h.authService()
	if !ok {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "accounts unavailable"})
		return
	}

	var creds domain.Credentials
	if err := c.ShouldBindJSON(&creds); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	user, token, expiresAt, err := auth.Register(c.Request.Context(), creds.Email, creds.Password)
	switch {
	case err == nil:
	case errors.Is(err, service.ErrInvalidEmail), errors.Is(err, service.ErrWeakPassword):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	case strings.Contains(err.Error(), "already registered"):
		c.JSON(http.StatusConflict, gin.H{"error": "email already registered"})
		return
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not create account"})
		return
	}

	setSessionCookie(c, token, expiresAt)
	c.JSON(http.StatusCreated, gin.H{"data": user})
}

func (h *Router) login(c *gin.Context) {
	auth, ok := h.authService()
	if !ok {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "accounts unavailable"})
		return
	}

	var creds domain.Credentials
	if err := c.ShouldBindJSON(&creds); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	user, token, expiresAt, err := auth.Login(c.Request.Context(), creds.Email, creds.Password)
	if err != nil {
		// One message for a missing account and a wrong password alike —
		// telling them apart would let anyone test which addresses exist.
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid email or password"})
		return
	}

	setSessionCookie(c, token, expiresAt)
	c.JSON(http.StatusOK, gin.H{"data": user})
}

func (h *Router) logout(c *gin.Context) {
	if auth, ok := h.authService(); ok {
		if token, err := c.Cookie(sessionCookieName); err == nil {
			_ = auth.Logout(c.Request.Context(), token)
		}
	}
	clearSessionCookie(c)
	c.JSON(http.StatusOK, gin.H{"data": gin.H{"signed_out": true}})
}

// me tells the frontend who it is talking to. Anonymous is a 200 with a null
// user rather than a 401: the page always calls this on load, and a 401 in
// the console on every anonymous visit trains people to ignore real ones.
func (h *Router) me(c *gin.Context) {
	user, ok := h.currentUser(c)
	if !ok {
		c.JSON(http.StatusOK, gin.H{"data": nil})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": user})
}

// userCount is the registered-account total. Public on purpose: it's the
// number the founders quote to investors, and it reveals nothing about who
// registered.
func (h *Router) userCount(c *gin.Context) {
	auth, ok := h.authService()
	if !ok {
		c.JSON(http.StatusOK, gin.H{"data": gin.H{"users": 0}})
		return
	}
	count, err := auth.CountUsers(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not count users"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{"users": count}})
}

// NewsAdminService is the moderation surface. Kept separate from
// AuthService so a fake in tests can implement one without the other.
type NewsAdminService interface {
	DeleteNewsItem(ctx context.Context, id int64) (bool, error)
}

func (h *Router) deleteNewsItem(c *gin.Context) {
	admin, ok := h.svc.(NewsAdminService)
	if !ok {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "news moderation unavailable"})
		return
	}

	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid news id"})
		return
	}

	deleted, err := admin.DeleteNewsItem(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not delete article"})
		return
	}
	if !deleted {
		c.JSON(http.StatusNotFound, gin.H{"error": "no such article"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{"deleted": id}})
}

// lockedTopN is how many leaderboard places are hidden from visitors without
// an account. The top of the table is the part worth signing up to see.
const lockedTopN = 5

// maskLockedPlayers blanks the identifying and scoring fields of the top N
// entries for anonymous callers.
//
// This has to happen server-side. Blurring in CSS would leave the real names
// and scores sitting in the JSON, one devtools tab away — which is both a
// hollow gate and an embarrassing one to have pointed out. The rows are kept
// (so the table still shows there are five places above) but carry nothing
// beyond their rank and a Locked flag.
//
// The caller must pass players already sorted by FRI descending, which is
// what ListPlayers returns.
func maskLockedPlayers(players []domain.PlayerWithScore, signedIn bool) []domain.PlayerWithScore {
	if signedIn || len(players) == 0 {
		return players
	}
	masked := make([]domain.PlayerWithScore, len(players))
	copy(masked, players)
	for i := range masked {
		if i >= lockedTopN {
			break
		}
		// Replace the row wholesale rather than blanking field by field. The
		// first version listed the fields to clear and missed Slug, which is
		// built from the player's name — "l-yamal" sitting in a masked row
		// named him as plainly as the name field would have. Rebuilding from
		// an empty struct means a field added later is withheld by default
		// instead of leaking until someone remembers to add it here.
		masked[i] = domain.PlayerWithScore{Locked: true}
	}
	return masked
}

// lockedPlayerIDs returns the player ids occupying the withheld places, taken
// from the same ordered list the leaderboard uses. Callers pass it to
// maskLockedNews so the gate covers the news feed as well: an article headlined
// with a player's name and face tells an anonymous visitor exactly who sits in
// the top five, which makes blanking the table rows pointless.
func lockedPlayerIDs(players []domain.PlayerWithScore) map[int64]bool {
	locked := make(map[int64]bool, lockedTopN)
	for i, p := range players {
		if i >= lockedTopN {
			break
		}
		locked[p.ID] = true
	}
	return locked
}

// maskLockedNews blanks the identifying parts of articles about withheld
// players. The article stays in the feed — the point is to show that coverage
// exists and is being scored, not to hide that there is news — but the player,
// the headline, the summary and the source come out, because any of them names
// the player just as clearly as the tag does.
//
// The impact delta stays: it carries no identity and it is the part that makes
// the case for signing up.
func maskLockedNews(items []domain.NewsItem, locked map[int64]bool, signedIn bool) []domain.NewsItem {
	if signedIn || len(locked) == 0 {
		return items
	}
	masked := make([]domain.NewsItem, len(items))
	copy(masked, items)
	for i := range masked {
		n := &masked[i]
		if n.PlayerID == nil || !locked[*n.PlayerID] {
			continue
		}
		n.Locked = true
		n.PlayerID = nil
		n.PlayerName = ""
		n.TitleEN = ""
		n.TitleRU = ""
		n.SummaryEN = ""
		n.SummaryRU = ""
		n.Source = ""
		n.SourceURL = ""
	}
	return masked
}

// lockedForCaller reports whether a given player id is one of the withheld
// places for this caller, and returns the ordered roster it used so callers
// that need it again don't query twice.
//
// Every per-player endpoint needs this. Before it existed, the gate lived
// only in the leaderboard handler, and /api/players/:id, its news and its
// history handed the same data straight back to anyone who guessed an id —
// which is any integer from 1 to 22.
func (h *Router) lockedForCaller(c *gin.Context, playerID int64) (bool, error) {
	if _, signedIn := h.currentUser(c); signedIn {
		return false, nil
	}
	players, err := h.svc.ListPlayers(c.Request.Context(), "", "", "")
	if err != nil {
		return false, err
	}
	return lockedPlayerIDs(players)[playerID], nil
}

// abortLocked answers a request for a withheld player. 404 rather than 403:
// a 403 confirms that the id exists and is interesting, which is most of what
// an anonymous caller wanted to learn.
func abortLocked(c *gin.Context) {
	c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "not found"})
}

// maskLockedEvents blanks the identity carried by pending rating events. The
// event itself stays votable-looking, but PlayerName and NewsTitle both name
// the player outright.
func maskLockedEvents(events []domain.PendingEvent, locked map[int64]bool, signedIn bool) []domain.PendingEvent {
	if signedIn || len(locked) == 0 {
		return events
	}
	masked := make([]domain.PendingEvent, len(events))
	copy(masked, events)
	for i := range masked {
		e := &masked[i]
		if !locked[e.PlayerID] {
			continue
		}
		e.Locked = true
		e.PlayerID = 0
		e.PlayerName = ""
		e.NewsTitle = ""
		e.NewsItemID = nil
	}
	return masked
}
