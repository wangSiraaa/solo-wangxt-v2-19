package handler

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"license/internal/httpx"
	"license/internal/service"
)

type Handler struct {
	svc *service.LicenseService
}

func New(svc *service.LicenseService) *Handler { return &Handler{svc: svc} }

func (h *Handler) Register(r *gin.Engine) {
	api := r.Group("/api")
	{
		api.GET("/overview", h.getOverview)
		api.POST("/admin/sweep", h.sweep)

		api.POST("/departments", h.createDepartment)
		api.PATCH("/departments/:id/quota", h.setQuota)
		api.POST("/pools", h.createPool)
		api.PATCH("/pools/:id/seats", h.setPoolSeats)

		api.POST("/checkouts", h.checkout)
		api.POST("/heartbeat", h.heartbeat)
		api.POST("/returns", h.returnSeat)
	}
}

// -------------------------------------------------- 查询 / 回收

func (h *Handler) getOverview(c *gin.Context) {
	v, err := h.svc.GetOverview(c.Request.Context())
	if err != nil {
		httpx.Internal(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"departments": v})
}

func (h *Handler) sweep(c *gin.Context) {
	res, err := h.svc.SweepExpired(c.Request.Context(), time.Now())
	if err != nil {
		httpx.Internal(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, res)
}

// -------------------------------------------------- 部门 / 池

type deptReq struct {
	Name  string `json:"name" binding:"required"`
	Quota int    `json:"quota"`
}

func (h *Handler) createDepartment(c *gin.Context) {
	var req deptReq
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.BadRequest(c, err.Error())
		return
	}
	d, err := h.svc.CreateDepartment(c.Request.Context(), req.Name, req.Quota)
	if err != nil {
		writeSvcError(c, err)
		return
	}
	c.JSON(http.StatusCreated, d)
}

type quotaReq struct {
	QuotaTotal int `json:"quota_total"`
}

func (h *Handler) setQuota(c *gin.Context) {
	id, err := parseInt64(c.Param("id"))
	if err != nil {
		httpx.BadRequest(c, "bad department id")
		return
	}
	var req quotaReq
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.BadRequest(c, err.Error())
		return
	}
	if req.QuotaTotal < 0 {
		httpx.BadRequest(c, "quota_total must be >= 0")
		return
	}
	d, err := h.svc.SetDepartmentQuota(c.Request.Context(), id, req.QuotaTotal)
	if err != nil {
		writeSvcError(c, err)
		return
	}
	c.JSON(http.StatusOK, d)
}

type poolReq struct {
	DepartmentID int64  `json:"department_id" binding:"required"`
	Product      string `json:"product" binding:"required"`
	Version      string `json:"version"`
	Seats        int    `json:"seats"`
}

func (h *Handler) createPool(c *gin.Context) {
	var req poolReq
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.BadRequest(c, err.Error())
		return
	}
	p, err := h.svc.CreatePool(c.Request.Context(), req.DepartmentID, req.Product, req.Version, req.Seats)
	if err != nil {
		writeSvcError(c, err)
		return
	}
	c.JSON(http.StatusCreated, p)
}

type seatsReq struct {
	Seats int `json:"seats"`
}

func (h *Handler) setPoolSeats(c *gin.Context) {
	id, err := parseInt64(c.Param("id"))
	if err != nil {
		httpx.BadRequest(c, "bad pool id")
		return
	}
	var req seatsReq
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.BadRequest(c, err.Error())
		return
	}
	p, err := h.svc.SetPoolSeats(c.Request.Context(), id, req.Seats)
	if err != nil {
		writeSvcError(c, err)
		return
	}
	c.JSON(http.StatusOK, p)
}

// -------------------------------------------------- 借 / 还

type checkoutReq struct {
	PoolID         int64  `json:"pool_id" binding:"required"`
	Employee       string `json:"employee" binding:"required"`
	Host           string `json:"host"`
	Mode           string `json:"mode"`
	TTLSeconds     int    `json:"ttl_seconds"` // 离线为借用时长,在线为心跳超时
	IdempotencyKey string `json:"idempotency_key"`
}

func (h *Handler) checkout(c *gin.Context) {
	var req checkoutReq
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.BadRequest(c, err.Error())
		return
	}
	if req.IdempotencyKey == "" {
		httpx.BadRequest(c, "idempotency_key is required (use a UUID; reuse it on network retry)")
		return
	}
	if req.Mode == "" {
		req.Mode = "OFFLINE"
	}
	if req.TTLSeconds <= 0 {
		if req.Mode == "OFFLINE" {
			req.TTLSeconds = 7 * 24 * 3600 // 离线默认 7 天
		} else {
			req.TTLSeconds = 300 // 在线默认 5 分钟心跳
		}
	}
	res, err := h.svc.Checkout(c.Request.Context(), service.CheckoutRequest{
		PoolID:         req.PoolID,
		Employee:       req.Employee,
		Host:           req.Host,
		Mode:           req.Mode,
		TTL:            time.Duration(req.TTLSeconds) * time.Second,
		IdempotencyKey: req.IdempotencyKey,
		ClientIP:       c.ClientIP(),
	})
	if err != nil {
		writeSvcError(c, err)
		return
	}
	// 重试回放同样返回 200,语义上就是"你之前的借用结果"
	c.JSON(http.StatusOK, res)
}

type returnReq struct {
	Credential string `json:"credential" binding:"required"`
}

func (h *Handler) heartbeat(c *gin.Context) {
	var req returnReq
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.BadRequest(c, err.Error())
		return
	}
	co, cred, err := h.svc.Heartbeat(c.Request.Context(), req.Credential, 300*time.Second)
	if err != nil {
		writeSvcError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"checkout": co, "credential": cred})
}

func (h *Handler) returnSeat(c *gin.Context) {
	var req returnReq
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.BadRequest(c, err.Error())
		return
	}
	co, err := h.svc.ReturnByCredential(c.Request.Context(), req.Credential)
	if err != nil {
		writeSvcError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"returned": co})
}

// -------------------------------------------------- 辅助

func parseInt64(s string) (int64, error) { return strconv.ParseInt(s, 10, 64) }

func writeSvcError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrNotFound):
		httpx.NotFound(c, err.Error())
	case errors.Is(err, service.ErrSeatsExhausted):
		httpx.Conflict(c, "seats_exhausted", err.Error())
	case errors.Is(err, service.ErrQuotaExceeded):
		httpx.Conflict(c, "quota_exceeded", err.Error())
	case errors.Is(err, service.ErrShrinkBelowUsage):
		httpx.Conflict(c, "shrink_below_usage", err.Error())
	case errors.Is(err, service.ErrDuplicateOnline):
		httpx.Conflict(c, "duplicate_online_seat", err.Error())
	case errors.Is(err, service.ErrBadCredential):
		httpx.Error(c, http.StatusUnauthorized, "bad_credential", err.Error())
	case errors.Is(err, service.ErrCredentialExpired):
		httpx.Conflict(c, "credential_expired", err.Error())
	case errors.Is(err, service.ErrNotActive):
		httpx.Conflict(c, "already_closed", err.Error())
	case errors.Is(err, service.ErrNonceMismatch):
		httpx.Error(c, http.StatusUnauthorized, "bad_credential", err.Error())
	case errors.Is(err, service.ErrOnlineOnly):
		httpx.BadRequest(c, err.Error())
	case errors.Is(err, service.ErrConflict):
		httpx.Conflict(c, "conflict", err.Error())
	default:
		httpx.Internal(c, err.Error())
	}
}
