package native

import (
	_ "embed"

	"github.com/gin-gonic/gin"
)

// The original management frontend and its feature modules are built together.
// Regenerate with scripts/build-native.ps1; never edit the bundled HTML by hand.
//
//go:embed web/management.html
var panel []byte

func (r *Runtime) ServePanel(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Referrer-Policy", "no-referrer")
	c.Header("Content-Security-Policy", "frame-ancestors 'none'; base-uri 'self'; object-src 'none'")
	c.Data(200, "text/html; charset=utf-8", panel)
}
