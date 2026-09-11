package api

import "github.com/gin-gonic/gin"

const authenticatedExtensionKey = "cliproxy.authenticated-extension"

// WithAuthenticatedMiddleware attaches host-owned admission after successful access authentication.
func WithAuthenticatedMiddleware(handler gin.HandlerFunc) ServerOption {
	return WithMiddleware(func(c *gin.Context) { c.Set(authenticatedExtensionKey, handler); c.Next() })
}
func continueAuthenticated(c *gin.Context) {
	if value, ok := c.Get(authenticatedExtensionKey); ok {
		if handler, ok := value.(gin.HandlerFunc); ok {
			handler(c)
			return
		}
	}
	c.Next()
}
