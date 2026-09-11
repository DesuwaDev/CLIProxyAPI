package api

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

type testManagementExtension struct{}

func (testManagementExtension) Register(g *gin.RouterGroup) {
	g.GET("/native/probe", func(c *gin.Context) { c.JSON(200, gin.H{"ok": true}) })
}
func (testManagementExtension) ServePanel(c *gin.Context) {
	c.Data(200, "text/html", []byte("native-panel"))
}

func TestNativeManagementUsesExistingAuthorization(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "native-test-secret")
	s := newTestServerWithOptions(t, WithManagementExtension(testManagementExtension{}))
	for _, tt := range []struct {
		key  string
		want int
	}{{"", 401}, {"test-key", 401}, {"native-test-secret", 200}} {
		req := httptest.NewRequest("GET", "/v0/management/native/probe", nil)
		req.RemoteAddr = "127.0.0.1:12345"
		if tt.key != "" {
			req.Header.Set("Authorization", "Bearer "+tt.key)
		}
		w := httptest.NewRecorder()
		s.engine.ServeHTTP(w, req)
		if w.Code != tt.want {
			t.Fatalf("status %d, want %d: %s", w.Code, tt.want, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	s.engine.ServeHTTP(w, httptest.NewRequest("GET", "/management.html", nil))
	if w.Code != 200 || w.Body.String() != "native-panel" {
		t.Fatalf("native panel not served: %d %s", w.Code, w.Body.String())
	}
}
