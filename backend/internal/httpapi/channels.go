package httpapi

import (
	"errors"
	"net/url"
	"slices"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"tokenswoop/backend/internal/gateway"
	"tokenswoop/backend/internal/security"
	"tokenswoop/backend/internal/store"
)

func validProtocol(protocol string) bool {
	return slices.Contains([]string{"openai", "openai_responses", "claude", "gemini"}, protocol)
}

// Reusing a stored credential is allowed only on its original origin.
func sameUpstreamOrigin(left, right string) bool {
	a, e1 := url.Parse(left)
	b, e2 := url.Parse(right)
	if e1 != nil || e2 != nil {
		return false
	}
	port := func(u *url.URL) string {
		if u.Port() != "" {
			return u.Port()
		}
		if u.Scheme == "https" {
			return "443"
		}
		return "80"
	}
	return a.Scheme == b.Scheme && strings.EqualFold(a.Hostname(), b.Hostname()) && port(a) == port(b)
}

func (a *App) channelForAdmin(c *gin.Context, id string) (store.Channel, bool) {
	if !validID(id) {
		fail(c, 400, "invalid_id", "Invalid channel.")
		return store.Channel{}, false
	}
	ch, e := a.Store.Channel(c, id)
	if errors.Is(e, pgx.ErrNoRows) {
		fail(c, 404, "not_found", "Channel not found.")
		return ch, false
	}
	if e != nil {
		fail(c, 503, "database_unavailable", "Channel unavailable.")
		return ch, false
	}
	return ch, true
}

func (a *App) saveChannel(c *gin.Context) {
	var in struct {
		store.Channel
		Secret        string                         `json:"secret"`
		CreateMissing bool                           `json:"create_missing_models"`
		Defaults      map[string]store.DraftMetadata `json:"model_defaults"`
	}
	if c.ShouldBindJSON(&in) != nil {
		fail(c, 400, "invalid_channel", "Provide valid channel details.")
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	in.BaseURL = strings.TrimRight(strings.TrimSpace(in.BaseURL), "/")
	in.Secret = strings.TrimSpace(in.Secret)
	if in.Name == "" || len(in.Name) > 100 || !gateway.ValidURL(in.BaseURL, a.Config.AllowPrivateUpstreams) || len(in.Models) > 500 || len(in.Defaults) > 500 || (in.Enabled && len(in.Models) == 0) || in.Priority < 0 || in.Priority > 10000 || len(in.Secret) > 8192 || strings.ContainsAny(in.Secret, "\r\n") {
		fail(c, 400, "invalid_channel", "Provide a name, valid API base URL and up to 500 model mappings. A channel without models must be disabled.")
		return
	}
	if !validProtocol(in.Protocol) {
		fail(c, 400, "invalid_protocol", "Unsupported upstream protocol.")
		return
	}
	if err := gateway.ValidateChannelOptions(in.Options); err != nil {
		fail(c, 400, "invalid_channel_options", err.Error())
		return
	}
	for public, upstream := range in.Models {
		if !modelID.MatchString(public) || !modelID.MatchString(upstream) {
			fail(c, 400, "invalid_mapping", "Model IDs must be 1–128 letters, digits, dots, slashes, colons, underscores, @ or hyphens.")
			return
		}
	}
	if id := c.Param("id"); id != "" {
		stored, ok := a.channelForAdmin(c, id)
		if !ok {
			return
		}
		in.ID = id
		if in.Revision != stored.Revision {
			fail(c, 409, "channel_changed", "This channel was changed elsewhere. Reload it before saving.")
			return
		}
		if in.Secret == "" && !sameUpstreamOrigin(stored.BaseURL, in.BaseURL) {
			fail(c, 400, "new_secret_required", "Enter an API key when changing the upstream host or port.")
			return
		}
	} else {
		if in.Secret == "" {
			fail(c, 400, "missing_secret", "An upstream API key is required.")
			return
		}
		in.ID = uuid.NewString()
		in.Revision = 0
	}
	if in.Secret != "" {
		cipher, e := security.Encrypt(a.Config.Secret, in.Secret)
		if e != nil {
			fail(c, 500, "encryption_failed", "Could not encrypt credentials.")
			return
		}
		in.SecretCipher = cipher
	}
	created, revision, e := a.Store.SaveChannelCatalog(c, currentUser(c).ID, in.Channel, in.CreateMissing, in.Defaults)
	if errors.Is(e, store.ErrChannelConflict) {
		fail(c, 409, "channel_changed", "This channel was changed elsewhere. Reload it before saving.")
		return
	}
	if errors.Is(e, store.ErrMissingModel) {
		fail(c, 400, "invalid_mapping", "Some public models do not exist. Enable draft creation or select existing models.")
		return
	}
	if e != nil {
		fail(c, 503, "database_unavailable", "Could not save channel.")
		return
	}
	c.JSON(200, gin.H{"id": in.ID, "revision": revision, "created_models": created})
}

func (a *App) fetchChannelModels(c *gin.Context) {
	var in struct {
		Options  *store.ChannelOptions `json:"options"`
		Protocol string                `json:"protocol"`
		BaseURL  string                `json:"base_url"`
		Secret   string                `json:"secret"`
	}
	if c.ShouldBindJSON(&in) != nil {
		fail(c, 400, "invalid_channel", "Provide channel connection details.")
		return
	}
	in.BaseURL = strings.TrimRight(strings.TrimSpace(in.BaseURL), "/")
	in.Secret = strings.TrimSpace(in.Secret)
	if id := c.Param("id"); id != "" {
		stored, ok := a.channelForAdmin(c, id)
		if !ok {
			return
		}
		if in.Protocol == "" {
			in.Protocol = stored.Protocol
		}
		if in.BaseURL == "" {
			in.BaseURL = stored.BaseURL
		}
		if in.Options == nil {
			in.Options = &stored.Options
		}
		if in.Secret == "" {
			if !sameUpstreamOrigin(stored.BaseURL, in.BaseURL) {
				fail(c, 400, "new_secret_required", "Enter an API key when changing the upstream host or port.")
				return
			}
			var e error
			in.Secret, e = security.Decrypt(a.Config.Secret, stored.SecretCipher)
			if e != nil {
				fail(c, 500, "decryption_failed", "Could not load channel credentials. Save a new API key.")
				return
			}
		}
	}
	if !validProtocol(in.Protocol) || !gateway.ValidURL(in.BaseURL, a.Config.AllowPrivateUpstreams) || in.Secret == "" || len(in.Secret) > 8192 || strings.ContainsAny(in.Secret, "\r\n") {
		fail(c, 400, "invalid_channel", "Choose a protocol and provide a valid API base URL and key.")
		return
	}
	allowed, rateErr := a.allow(c, "model-discovery:"+currentUser(c).ID, 10)
	if rateErr != nil {
		fail(c, 503, "rate_limiter_unavailable", "Model discovery is temporarily unavailable.")
		return
	}
	if !allowed {
		fail(c, 429, "rate_limited", "Model discovery is limited to 10 attempts per minute. Try again shortly.")
		return
	}
	if in.Options == nil {
		in.Options = &store.ChannelOptions{}
	}
	if err := gateway.ValidateChannelOptions(*in.Options); err != nil {
		fail(c, 400, "invalid_channel_options", err.Error())
		return
	}
	result, e := a.Gateway.DiscoverModels(c.Request.Context(), in.Protocol, in.BaseURL, in.Secret, *in.Options)
	if e != nil {
		var discovery *gateway.DiscoveryError
		if errors.As(e, &discovery) {
			status := 502
			if discovery.Code == "upstream_timeout" {
				status = 504
			}
			fail(c, status, discovery.Code, discovery.Message)
		} else {
			fail(c, 502, "discovery_failed", "Could not fetch upstream models.")
		}
		return
	}
	c.JSON(200, result)
}
