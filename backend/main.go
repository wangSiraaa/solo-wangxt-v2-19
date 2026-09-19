package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/gin-gonic/gin"

	"license/internal/config"
	"license/internal/db"
	"license/internal/handler"
	"license/internal/service"
)

func main() {
	cfg := config.Load()

	mdb, err := db.Open(cfg.MySQLDSN)
	if err != nil {
		log.Fatalf("mysql: %v", err)
	}
	if err := db.Migrate(mdb, "migrations"); err != nil {
		log.Fatalf("migrate: %v", err)
	}

	cache := service.NewCache(cfg.RedisAddr)
	if err := cache.Ping(context.Background()); err != nil {
		log.Fatalf("redis: %v", err)
	}

	svc := service.NewLicenseService(mdb, cache, cfg.CredentialHMAC)
	seed(svc)

	// 后台回收器:周期性把到期未还的席位(离线到期 / 在线心跳超时)收回池中。
	go runSweeper(svc, 30*time.Second)

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Logger(), gin.Recovery())

	handler.New(svc).Register(r)

	// 托管 React 构建产物(若存在)
	dist := "frontend-dist"
	if _, err := os.Stat(dist); err == nil {
		r.Static("/assets", filepath.Join(dist, "assets"))
		r.StaticFile("/favicon.ico", filepath.Join(dist, "favicon.ico"))
		r.NoRoute(func(c *gin.Context) {
			c.File(filepath.Join(dist, "index.html"))
		})
	}

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           r,
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Printf("license server listening on %s", cfg.HTTPAddr)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

// seed 写入演示用部门与许可证池(幂等:已存在则跳过)。
func seed(svc *service.LicenseService) {
	ctx := context.Background()
	overview, err := svc.GetOverview(ctx)
	if err != nil {
		log.Printf("seed: overview: %v", err)
		return
	}
	if len(overview) > 0 {
		return
	}
	cad, err := svc.CreateDepartment(ctx, "CAD设计部", 5)
	if err != nil {
		log.Printf("seed dept1: %v", err)
		return
	}
	cae, _ := svc.CreateDepartment(ctx, "仿真分析部", 3)

	if _, err := svc.CreatePool(ctx, cad.ID, "AutoCAD", "2025", 3); err != nil {
		log.Printf("seed pool1: %v", err)
	}
	if _, err := svc.CreatePool(ctx, cad.ID, "SolidWorks", "2024", 2); err != nil {
		log.Printf("seed pool2: %v", err)
	}
	if _, err := svc.CreatePool(ctx, cae.ID, "ANSYS", "2024R2", 3); err != nil {
		log.Printf("seed pool3: %v", err)
	}
	log.Println("seed data ready")
}

// runSweeper 周期性过期回收;离线/在线席位到期后无需凭证自动释放。
func runSweeper(svc *service.LicenseService, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for range t.C {
		res, err := svc.SweepExpired(context.Background(), time.Now())
		if err != nil {
			log.Printf("sweeper: %v", err)
			continue
		}
		if res.Reclaimed > 0 {
			log.Printf("sweeper: reclaimed %d expired seat(s)", res.Reclaimed)
		}
	}
}
