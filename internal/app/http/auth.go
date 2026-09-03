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
	DeleteNewsItem(ctx context.Context, id int64) (*domain.NewsDeletion, error)
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

	deletion, err := admin.DeleteNewsItem(c.Request.Context(), id)
	if errors.Is(err, service.ErrNewsNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "no such article"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not delete article"})
		return
	}
	// The moderator sees what the removal did to the score, not a bare OK.
	c.JSON(http.StatusOK, gin.H{"data": deletion})
}

// lockedTopN is how many leaderboard places are hidden from visitors without
// an account. The top of the table is the part worth signing up to see.
const lockedTopN = 5

// maskLockedPlayers withholds the top places from anonymous callers.
//
// It masks by identity, not by position in the slice handed to it. Masking
// indices 0..4 of a *filtered* result turned the filters into an oracle:
// ?club=Real+Madrid returned three rows where only one Real Madrid player was
// nameable, so exactly two of the withheld five played for Real Madrid, and
// sweeping clubs and positions reconstructed every hidden slot. ?search=Mbappé
// answered the membership question outright.
//
// So the withheld set is computed once from the unfiltered roster and matched
// by id. `filtered` says whether the caller narrowed the list: on the plain
// leaderboard the placeholder rows stay, because the ranks above are part of
// what the page is showing. On a filtered request they are dropped entirely —
// a blank row there would still answer "how many of your hidden five match
// this club", which is the question we are refusing.
//
// This has to happen server-side. Blurring in CSS would leave the names and
// scores in the JSON one devtools tab away.
func maskLockedPlayers(players []domain.PlayerWithScore, locked map[int64]bool, signedIn, filtered bool) []domain.PlayerWithScore {
	if signedIn || len(players) == 0 {
		return players
	}
	masked := make([]domain.PlayerWithScore, 0, len(players))
	for _, p := range players {
		if !locked[p.ID] {
			masked = append(masked, p)
			continue
		}
		if filtered {
			continue
		}
		// Rebuild from an empty struct rather than clearing a list of fields.
		// The first version cleared by hand and missed Slug — which is built
		// from the player's name, so "l-yamal" sat in a row meant to hide
		// them. Starting from zero means a field added to the model later is
		// withheld by default instead of leaking until someone remembers it.
		masked = append(masked, domain.PlayerWithScore{Locked: true})
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

// lockedPlayerNames mirrors lockedPlayerIDs for the places that only have a
// name to go on. news_items keeps a denormalized player_name beside the
// foreign key, and the key is only set when the seed matched a name exactly —
// so an article can name a withheld player while its player_id is NULL, and a
// mask keyed on the id alone waves it straight through.
func lockedPlayerNames(players []domain.PlayerWithScore) map[string]bool {
	locked := make(map[string]bool, lockedTopN)
	for i, p := range players {
		if i >= lockedTopN {
			break
		}
		if key := normalizePlayerKey(p.Name); key != "" {
			locked[key] = true
		}
	}
	return locked
}

// normalizePlayerKey lowercases, folds the diacritics our roster actually
// contains and drops punctuation, so "K. Mbappé" and "k mbappe" compare equal.
func normalizePlayerKey(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	replacer := strings.NewReplacer(
		"á", "a", "à", "a", "â", "a", "ä", "a", "ã", "a", "å", "a",
		"é", "e", "è", "e", "ê", "e", "ë", "e",
		"í", "i", "ì", "i", "î", "i", "ï", "i",
		"ó", "o", "ò", "o", "ô", "o", "ö", "o", "õ", "o",
		"ú", "u", "ù", "u", "û", "u", "ü", "u",
		"ñ", "n", "ç", "c", "š", "s", "ć", "c", "č", "c", "ž", "z",
		".", " ", "-", " ", "'", " ", "’", " ",
	)
	return strings.Join(strings.Fields(replacer.Replace(name)), " ")
}

// ErrGateUnavailable is returned when the withheld set can't be established —
// an empty roster, typically mid-seed. Callers answer 503 rather than serving
// an ungated response: "we could not work out what to hide" must never quietly
// become "nothing is hidden".
var ErrGateUnavailable = errors.New("cannot establish leaderboard gate")

// maskLockedNews blanks the identifying parts of articles about withheld
// players. The article stays in the feed — the point is to show that coverage
// exists and is being scored, not to hide that there is news — but the player,
// the headline, the summary and the source come out, because any of them names
// the player just as clearly as the tag does.
//
// The impact delta stays: it carries no identity and it is the part that makes
// the case for signing up.
func maskLockedNews(items []domain.NewsItem, locked map[int64]bool, lockedNames map[string]bool, signedIn bool) []domain.NewsItem {
	if signedIn {
		return items
	}
	masked := make([]domain.NewsItem, len(items))
	copy(masked, items)
	for i := range masked {
		n := masked[i]
		byID := n.PlayerID != nil && locked[*n.PlayerID]
		byName := lockedNames[normalizePlayerKey(n.PlayerName)]
		if !byID && !byName {
			continue
		}
		// Rebuild from an empty struct rather than clearing fields one by
		// one. The player mask learned this the hard way — its denylist
		// missed Slug — and the same applies here: published_at with an
		// impact_delta is a fingerprint that identifies the article, and so
		// the player, in one search.
		masked[i] = domain.NewsItem{
			Locked: true,
			ID:     n.ID,
			// The delta is what makes the case for signing up and names
			// nobody. The impact type keeps the card's colour honest.
			ImpactType:  n.ImpactType,
			ImpactDelta: n.ImpactDelta,
		}
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
	if len(players) == 0 {
		// An empty roster means we could not work out what to withhold — a
		// seed running mid-request truncates the table inside its
		// transaction. Treating that as "nothing is locked" would swing the
		// gate wide open exactly when the data is in flux.
		return false, ErrGateUnavailable
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
func maskLockedEvents(events []domain.PendingEvent, locked map[int64]bool, lockedNames map[string]bool, signedIn bool) []domain.PendingEvent {
	if signedIn {
		return events
	}
	masked := make([]domain.PendingEvent, len(events))
	copy(masked, events)
	for i := range masked {
		e := masked[i]
		if !locked[e.PlayerID] && !lockedNames[normalizePlayerKey(e.PlayerName)] {
			continue
		}
		// Keep only what a vote needs. trigger_word is dropped along with the
		// name: "doping" or "hat_trick" against a withheld place narrows the
		// player as effectively as naming them.
		masked[i] = domain.PendingEvent{
			Locked:         true,
			ID:             e.ID,
			ProposedDelta:  e.ProposedDelta,
			VotesCount:     e.VotesCount,
			VotesMedian:    e.VotesMedian,
			VotingClosesAt: e.VotingClosesAt,
		}
	}
	return masked
}

// RosterAdminService is the add-player surface. Separate from the main
// Service interface so router tests that don't exercise it need not implement
// it.
type RosterAdminService interface {
	AddPlayer(ctx context.Context, input domain.AddPlayerInput) (*domain.PlayerWithScore, error)
}

func (h *Router) addPlayer(c *gin.Context) {
	roster, ok := h.svc.(RosterAdminService)
	if !ok {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "roster management unavailable"})
		return
	}

	var input domain.AddPlayerInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	player, err := roster.AddPlayer(c.Request.Context(), input)
	if err == nil {
		c.JSON(http.StatusCreated, gin.H{"data": player})
		return
	}

	// Several people match: hand back the candidates and let the operator pick
	// by provider id. Answering with a guess is how the wrong García got in.
	var ambiguous *service.AmbiguousPlayerError
	if errors.As(err, &ambiguous) {
		c.JSON(http.StatusConflict, gin.H{
			"error":      "several players match — repeat the request with provider_player_id",
			"candidates": ambiguous.Candidates,
		})
		return
	}

	switch {
	case errors.Is(err, service.ErrPlayerExists):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
	case errors.Is(err, service.ErrPlayerNotFound), errors.Is(err, service.ErrClubUnknown):
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
	case errors.Is(err, service.ErrProviderRequired):
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
	}
}
