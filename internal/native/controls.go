package native

import (
	"context"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/event"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/httpx"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/inventory"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/risk"
	auth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	ex "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type riskObservationKey struct{}

type Identity struct {
	ID          string `json:"id"`
	Provider    string `json:"provider"`
	Fingerprint bool   `json:"fingerprint"`
	Headers     bool   `json:"headers"`
}

// SetIdentityLookup is called once by the host before routes and execution start.
func (r *Runtime) SetIdentityLookup(lookup func(string) (Identity, bool)) {
	r.identityLookup = lookup
	r.fingerprint.Eligible = func(id string) bool { v, ok := lookup(id); return ok && v.Fingerprint }
	r.headers.Eligible = func(id string) bool { v, ok := lookup(id); return ok && v.Headers }
}
func (r *Runtime) registerIdentity(g *gin.RouterGroup) {
	g.POST("/identity", func(c *gin.Context) {
		var input struct {
			Key        string `json:"key"`
			Credential string `json:"credential"`
		}
		if !httpx.Decode(c, &input) {
			return
		}
		if input.Key != "" && input.Credential == "" {
			c.JSON(200, Identity{ID: event.KeyHash(input.Key)})
			return
		}
		if input.Key == "" && input.Credential != "" && r.identityLookup != nil {
			if id, ok := r.identityLookup(input.Credential); ok {
				c.JSON(200, id)
				return
			}
		}
		c.JSON(404, gin.H{"error": "credential not found; save it first"})
	})
}
func (r *Runtime) CallerMiddleware() gin.HandlerFunc {
	limited := r.limits.CallerMiddleware(func() bool { return r.enabled("limits") })
	return func(c *gin.Context) {
		if r.enabled("inventory") {
			key := event.KeyHash(c.GetString("userApiKey"))
			if err := r.inventory.Check("client", key, ""); err != nil {
				if blocked, ok := err.(*inventory.Rejection); ok {
					c.Data(403, "application/json", blocked.ResponseBody())
					c.Abort()
					return
				}
			}
		}
		limited(c)
	}
}
func (r *Runtime) BeforeExecute(ctx context.Context, a *auth.Auth, req ex.Request, opts ex.Options) (context.Context, func(), error) {
	release := func() {}
	if a == nil {
		return ctx, release, nil
	}
	if r.enabled("inventory") {
		key := riskInput(ctx, a, req, opts).KeyHash
		model := req.Model
		if requested, ok := opts.Metadata[ex.RequestedModelMetadataKey].(string); ok && requested != "" {
			model = requested
		}
		if err := r.inventory.Check("client", key, model); err != nil {
			return ctx, nil, err
		}
		if err := r.inventory.Check("credential", a.Index, req.Model); err != nil {
			return ctx, nil, err
		}
	}
	if r.enabled("risk") {
		input := riskInput(ctx, a, req, opts)
		observation := input
		observation.Body = nil
		ctx = context.WithValue(ctx, riskObservationKey{}, observation)
		if err := r.risk.Check(ctx, input); err != nil {
			return ctx, nil, err
		}
	}
	if r.enabled("limits") {
		var err error
		release, err = r.limits.Acquire(ctx, "credential", a.Index)
		if err != nil {
			return ctx, nil, err
		}
	}
	if r.enabled("headers") && a.Provider == "codex" && a.AuthKind() == auth.AuthKindOAuth {
		if transform := r.headers.Transform(a.Index, opts.Headers); transform != nil {
			ctx = ex.WithOutboundHeaderTransform(ctx, transform)
		}
	}
	if r.enabled("fingerprint") && a.Provider == "codex" && a.AuthKind() == auth.AuthKindOAuth {
		original := opts.OriginalRequest
		if len(original) == 0 {
			original = req.Payload
		}
		caller, _ := opts.Metadata[ex.CallerScopeMetadataKey].(string)
		if caller == "" {
			caller, _ = req.Metadata[ex.CallerScopeMetadataKey].(string)
		}
		if transform := r.fingerprint.Transform(a.Index, caller, original, opts.Headers); transform != nil {
			ctx = ex.WithOutboundTransform(ctx, transform)
		}
	}
	return ctx, release, nil
}

func riskInput(ctx context.Context, a *auth.Auth, req ex.Request, opts ex.Options) risk.Input {
	key := ""
	if c, ok := ctx.Value("gin").(*gin.Context); ok && c != nil {
		key = event.KeyHash(c.GetString("userApiKey"))
	}
	if key == "" {
		key, _ = opts.Metadata[ex.CallerScopeMetadataKey].(string)
	}
	session, _ := opts.Metadata[ex.CanonicalSessionIDMetadataKey].(string)
	if session == "" {
		session, _ = opts.Metadata[ex.ExecutionSessionMetadataKey].(string)
	}
	if session == "" {
		session, _ = opts.Metadata[ex.DerivedSessionIDMetadataKey].(string)
	}
	return risk.Input{Body: req.Payload, Provider: a.Provider, Model: req.Model, Account: a.Index, KeyHash: key, Session: session, TraceID: logging.ObservationID(ctx)}
}
func (r *Runtime) ObserveExecution(ctx context.Context, a *auth.Auth, req ex.Request, opts ex.Options, err error) {
	if r.enabled("risk") && a != nil {
		if input, ok := ctx.Value(riskObservationKey{}).(risk.Input); ok {
			r.risk.ObserveUpstream(ctx, input, err)
		}
	}
}

func (r *Runtime) SetInventoryLookup(lookup func() map[string]inventory.Account) {
	r.inventory.Accounts = lookup
}
