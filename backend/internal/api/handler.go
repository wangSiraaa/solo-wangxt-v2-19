// Package api 提供 Gin HTTP 接口。
package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"licensepool/internal/cache"
	"licensepool/internal/license"
	"licensepool/internal/store"
)

const poolCacheKey = "cache:pool:v1"

type Handler struct {
	svc   *license.Service
	cache *cache.Cache
}

func New(svc *license.Service, c *cache.Cache) *Handler {
	return &Handler{svc: svc, cache: c}
}

// Register 注册路由。
func (h *Handler) Register(r *gin.Engine) {
	api := r.Group("/api")
	{
		api.GET("/pool", h.getPool)
		api.POST("/borrow", h.borrow)
		api.POST("/return", h.postReturn)
		api.POST("/heartbeat", h.heartbeat)
		api.PUT("/departments/:id/quota", h.adjustQuota)
		api.POST("/admin/reap", h.manualReap) // 演示用：立即触发过期回收
	}
}

func (h *Handler) invalidate(c *gin.Context) {
	h.cache.Invalidate(c.Request.Context(), poolCacheKey)
}

// GET /api/pool 池视图：部门额度 + 占用来源（走 Redis 缓存）。
func (h *Handler) getPool(c *gin.Context) {
	if raw, ok := h.cache.Get(c.Request.Context(), poolCacheKey); ok {
		var cached store.PoolView
		if err := json.Unmarshal([]byte(raw), &cached); err == nil {
			cached.Cached = true
			c.JSON(http.StatusOK, cached)
			return
		}
	}
	view, err := h.svc.PoolView(c.Request.Context())
	if err != nil {
		writeError(c, err)
		return
	}
	if buf, err := json.Marshal(view); err == nil {
		h.cache.Set(c.Request.Context(), poolCacheKey, string(buf))
	}
	c.JSON(http.StatusOK, view)
}

func (h *Handler) borrow(c *gin.Context) {
	var req license.BorrowRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": license.CodeInvalidParam, "message": err.Error()})
		return
	}
	res, err := h.svc.Borrow(c.Request.Context(), req)
	if err != nil {
		writeError(c, err)
		return
	}
	h.invalidate(c)
	c.JSON(http.StatusOK, res)
}

type returnReq struct {
	Credential string `json:"credential" binding:"required"`
}

func (h *Handler) postReturn(c *gin.Context) {
	var req returnReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": license.CodeInvalidParam, "message": err.Error()})
		return
	}
	res, err := h.svc.Return(c.Request.Context(), req.Credential)
	if err != nil {
		writeError(c, err)
		return
	}
	h.invalidate(c)
	// 过期/重复归还属于业务语义结果（不是 5xx），用 200 返回结构化状态，
	// 前端据此提示"席位已回收"或"凭证已使用"。
	c.JSON(http.StatusOK, res)
}

type heartbeatReq struct {
	Credential string `json:"credential" binding:"required"`
	TTLSecond  int64  `json:"ttlSecond"`
}

func (h *Handler) heartbeat(c *gin.Context) {
	var req heartbeatReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": license.CodeInvalidParam, "message": err.Error()})
		return
	}
	if req.TTLSecond == 0 {
		req.TTLSecond = 60
	}
	res, err := h.svc.Heartbeat(c.Request.Context(), req.Credential, req.TTLSecond)
	if err != nil {
		writeError(c, err)
		return
	}
	h.invalidate(c)
	c.JSON(http.StatusOK, res)
}

type quotaReq struct {
	Quota int `json:"quota"`
}

func (h *Handler) adjustQuota(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": license.CodeInvalidParam, "message": "部门 ID 非法"})
		return
	}
	var req quotaReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": license.CodeInvalidParam, "message": err.Error()})
		return
	}
	oc, err := h.svc.AdjustQuota(c.Request.Context(), id, req.Quota)
	if err != nil {
		writeError(c, err)
		return
	}
	h.invalidate(c)
	c.JSON(http.StatusOK, gin.H{
		"message":    "额度已调整；缩减不会抹掉现存借用，只限制新申请",
		"adjustedAt": time.Now(),
		"department": oc,
	})
}

func (h *Handler) manualReap(c *gin.Context) {
	n, err := h.svc.ReapExpired(c.Request.Context())
	if err != nil {
		writeError(c, err)
		return
	}
	h.invalidate(c)
	c.JSON(http.StatusOK, gin.H{"reaped": n})
}

// writeError 把 license.Error 映射为合适的 HTTP 状态。
func writeError(c *gin.Context, err error) {
	var le *license.Error
	if errors.As(err, &le) {
		status := http.StatusInternalServerError
		switch le.Code {
		case license.CodeInvalidParam:
			status = http.StatusBadRequest
		case license.CodeNoSeat, license.CodeRequestConflict:
			status = http.StatusConflict // 409：最后一席竞争失败
		case license.CodeBadCredential, license.CodeHeartbeatDenied:
			status = http.StatusForbidden // 403：凭证无效/不允许
		case license.CodeExpiredReclaim:
			status = http.StatusGone // 410：已过期回收
		case license.CodeNotFound:
			status = http.StatusNotFound
		}
		c.JSON(status, gin.H{"code": le.Code, "message": le.Message})
		return
	}
	c.JSON(http.StatusInternalServerError, gin.H{"code": "INTERNAL", "message": err.Error()})
}
