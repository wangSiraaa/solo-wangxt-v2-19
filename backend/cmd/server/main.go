// licensepool 服务入口：连接 MySQL/Redis，启动过期回收循环与 HTTP 服务。
package main

import (
	"context"
	"database/sql"
	"embed"
	"io/fs"
	"log"
	"net/http"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	_ "github.com/go-sql-driver/mysql"

	"licensepool/internal/api"
	"licensepool/internal/cache"
	"licensepool/internal/config"
	"licensepool/internal/credential"
	"licensepool/internal/license"
	"licensepool/internal/store"
)

//go:embed all:web
var webFS embed.FS

func main() {
	cfg := config.Load()

	// MySQL：核心交易存储。
	db, err := sql.Open("mysql", cfg.MySQLDSN)
	if err != nil {
		log.Fatalf("打开 MySQL 失败: %v", err)
	}
	db.SetMaxOpenConns(32)
	db.SetMaxIdleConns(8)
	db.SetConnMaxLifetime(time.Hour)

	if err := waitForDB(db); err != nil {
		log.Fatalf("连接 MySQL 失败: %v", err)
	}
	store.MustExec(db)
	log.Println("MySQL 就绪，表结构已确保存在")

	svc := license.NewService(db, credential.NewSigner(cfg.HMACSecret))
	if cfg.Seed {
		if err := svc.Seed(context.Background()); err != nil {
			log.Fatalf("写入演示数据失败: %v", err)
		}
		log.Println("演示部门数据已就绪")
	}

	// Redis：仅缓存查询；连不上则降级（不阻断服务）。
	rc, err := cache.New(cfg.RedisAddr, cfg.RedisPassword, cfg.RedisDB, cfg.CacheTTL)
	if err != nil {
		log.Printf("警告: Redis 不可用，查询将直查 MySQL: %v", err)
		rc = nil
	} else {
		log.Printf("Redis 就绪（缓存 TTL=%s）", cfg.CacheTTL)
	}
	if rc != nil {
		defer rc.Close()
	}

	// 过期回收循环：到期席位（含离线）由系统统一回收。
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go runReaper(ctx, svc, cfg.ReapInterval)

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(requestLogger())

	api.New(svc, rc).Register(r)
	registerFrontend(r)

	srv := &http.Server{Addr: cfg.HTTPAddr, Handler: r}
	go func() {
		log.Printf("HTTP 服务监听 %s", cfg.HTTPAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP 服务退出: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("正在关闭服务...")
	shCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shCtx)
}

func waitForDB(db *sql.DB) error {
	deadline := time.Now().Add(30 * time.Second)
	var err error
	for time.Now().Before(deadline) {
		if err = db.Ping(); err == nil {
			return nil
		}
		time.Sleep(time.Second)
	}
	return err
}

func runReaper(ctx context.Context, svc *license.Service, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			n, err := svc.ReapExpired(cctx)
			cancel()
			if err != nil {
				log.Printf("过期回收出错: %v", err)
				continue
			}
			if n > 0 {
				log.Printf("已回收 %d 个到期席位", n)
			}
		}
	}
}

func requestLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		log.Printf("%s %s -> %d (%s)", c.Request.Method, c.Request.URL.Path,
			c.Writer.Status(), time.Since(start).Round(time.Millisecond))
	}
}

// registerFrontend 托管前端构建产物（web/dist），未构建时返回提示页。
func registerFrontend(r *gin.Engine) {
	dist, err := fs.Sub(webFS, "web/dist")
	if err != nil {
		return
	}
	if _, err := fs.Stat(dist, "index.html"); err != nil {
		r.NoRoute(func(c *gin.Context) {
			c.Data(http.StatusOK, "text/plain; charset=utf-8",
				[]byte("licensepool API 已启动。前端尚未构建：请在 frontend/ 执行 npm install && npm run build。"))
		})
		return
	}
	fileServer := http.FileServer(http.FS(dist))
	r.NoRoute(func(c *gin.Context) {
		// API 未命中返回 404 JSON；其余路径交给 SPA，找不到文件回退 index.html。
		p := c.Request.URL.Path
		if len(p) >= 4 && p[:4] == "/api" {
			c.JSON(http.StatusNotFound, gin.H{"code": "NOT_FOUND", "message": "接口不存在"})
			return
		}
		if _, err := fs.Stat(dist, strings.TrimPrefix(p, "/")); err != nil {
			c.Request.URL.Path = "/"
		}
		fileServer.ServeHTTP(c.Writer, c.Request)
	})
}
