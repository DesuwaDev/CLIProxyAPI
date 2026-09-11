package httpx

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

func Error(c *gin.Context, err error) {
	log.WithError(err).Warn("native management operation failed")
	c.JSON(http.StatusInternalServerError, gin.H{"error": "Native management operation failed; see server logs."})
}

func Decode(c *gin.Context, value any) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1024*1024)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	err := decoder.Decode(value)
	if err == nil {
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			err = io.ErrUnexpectedEOF
		}
	}
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid JSON or request exceeds 1 MiB."})
		return false
	}
	return true
}
