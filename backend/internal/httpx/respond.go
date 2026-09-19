package httpx

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// Err 统一错误响应体。
type Err struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func Error(c *gin.Context, status int, code, msg string) {
	c.AbortWithStatusJSON(status, Err{Code: code, Message: msg})
}

func BadRequest(c *gin.Context, msg string)     { Error(c, http.StatusBadRequest, "bad_request", msg) }
func NotFound(c *gin.Context, msg string)       { Error(c, http.StatusNotFound, "not_found", msg) }
func Conflict(c *gin.Context, code, msg string) { Error(c, http.StatusConflict, code, msg) }
func Internal(c *gin.Context, msg string)       { Error(c, http.StatusInternalServerError, "internal", msg) }
