// corral-relay 是手机端的中继服务。
//
// 默认单租户：一个二进制 + JSON 状态文件。加 --multi-tenant 则接 Postgres、
// GitHub 设备码登录与配额，给公共实例用。
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/corral-dev/corral-relay/internal/hub"
	"github.com/corral-dev/corral-relay/internal/metrics"
	"github.com/corral-dev/corral-relay/internal/push"
	"github.com/corral-dev/corral-relay/internal/server"
	"github.com/corral-dev/corral-relay/internal/store"
	"github.com/corral-dev/corral-relay/internal/store/file"
	"github.com/corral-dev/corral-relay/internal/store/postgres"
	"github.com/corral-dev/corral-relay/internal/web"
)

const (
	defaultStatePath = "/var/lib/corral-relay/hosts.json"
	legacyStatePath  = "/var/lib/pickup-relay/hosts.json"
)

func main() {
	addr := flag.String("addr", envOrFirst("CORRAL_RELAY_ADDR", "PICKUP_RELAY_ADDR", ":8788"), "监听地址")
	adminAddr := flag.String("admin-addr", envOr("CORRAL_RELAY_ADMIN_ADDR", "127.0.0.1:8789"), "admin 监听（/metrics），默认只绑本机")
	statePath := flag.String("state", envOrFirst("CORRAL_RELAY_STATE", "PICKUP_RELAY_STATE", defaultStatePath), "单租户开发机登记文件")
	multi := flag.Bool("multi-tenant", envOr("CORRAL_RELAY_MULTI_TENANT", "") == "1", "多租户模式（需要 Postgres 与 GitHub OAuth）")
	dbURL := flag.String("database-url", envOr("CORRAL_DATABASE_URL", ""), "Postgres URL")
	publicURL := flag.String("public-url", envOr("CORRAL_PUBLIC_URL", ""), "对外 HTTPS 根地址（多租户必填，填你自己的域名）")
	githubID := flag.String("github-client-id", envOr("CORRAL_GITHUB_CLIENT_ID", ""), "GitHub OAuth client id")
	githubSecret := flag.String("github-client-secret", envOr("CORRAL_GITHUB_CLIENT_SECRET", ""), "GitHub OAuth secret")
	sessionSecret := flag.String("session-secret", envOr("CORRAL_SESSION_SECRET", ""), "Web 会话 HMAC 密钥")
	nodeID := flag.String("node-id", envOr("CORRAL_RELAY_NODE_ID", hostname()), "本实例节点标识")
	pushKey := flag.String("push-key", envOrFirst("CORRAL_PUSH_KEY", "PICKUP_PUSH_KEY", ""), "苹果推送 p8 密钥文件")
	pushKeyID := flag.String("push-key-id", envOrFirst("CORRAL_PUSH_KEY_ID", "PICKUP_PUSH_KEY_ID", ""), "苹果推送密钥编号")
	pushTeamID := flag.String("push-team-id", envOrFirst("CORRAL_PUSH_TEAM_ID", "PICKUP_PUSH_TEAM_ID", ""), "苹果开发者团队编号")
	pushBundleID := flag.String("push-bundle-id", envOrFirst("CORRAL_PUSH_BUNDLE_ID", "PICKUP_PUSH_BUNDLE_ID", "com.x0c.corral"), "默认 App 标识")
	pushProfiles := flag.String("push-profiles", envOr("CORRAL_PUSH_PROFILES", ""), "多档推送 JSON 文件")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	var st store.Store
	if *multi {
		if *dbURL == "" {
			log.Error("多租户模式需要 CORRAL_DATABASE_URL")
			os.Exit(1)
		}
		pg, err := postgres.Open(context.Background(), *dbURL)
		if err != nil {
			log.Error("打开 Postgres 失败", "error", err)
			os.Exit(1)
		}
		st = pg
		log.Info("多租户存储已就绪")
	} else {
		hosts, err := file.Open(resolveStatePath(*statePath, defaultStatePath, legacyStatePath))
		if err != nil {
			log.Error("读取开发机注册信息失败", "error", err)
			os.Exit(1)
		}
		st = hosts
	}

	router := push.NewRouter(nil)
	pushConfig := push.Config{KeyPath: *pushKey, KeyID: *pushKeyID, TeamID: *pushTeamID, BundleID: *pushBundleID}
	if pushConfig.Enabled() {
		apns, err := push.New(pushConfig)
		if err != nil {
			log.Error("推送初始化失败", "error", err)
			os.Exit(1)
		}
		router = push.NewRouter(apns)
		router.Add(pushConfig.BundleID, apns)
		log.Info("推送已启用", "bundle", pushConfig.BundleID)
	} else {
		log.Info("推送未配置，中继只做转发")
	}
	if *pushProfiles != "" {
		cfgs, err := push.LoadProfilesFile(*pushProfiles)
		if err != nil {
			log.Error("读取推送多档失败", "error", err)
			os.Exit(1)
		}
		for _, cfg := range cfgs {
			sender, err := push.New(cfg)
			if err != nil {
				log.Error("推送档初始化失败", "bundle", cfg.BundleID, "error", err)
				os.Exit(1)
			}
			router.Add(cfg.BundleID, sender)
		}
	}

	central := hub.New(router)
	central.SetByteRecorder(func(accountID string, n int64) {
		st.AddBytes(accountID, n)
	})

	opts := server.Options{
		MultiTenant:  *multi,
		NodeID:       *nodeID,
		PublicURL:    *publicURL,
		GitHubID:     *githubID,
		GitHubSecret: *githubSecret,
		Cookies:      web.NewCookieJar([]byte(*sessionSecret)),
	}
	httpServer := &http.Server{
		Addr:              *addr,
		Handler:           server.New(central, st, log, opts).Routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go flushPeriodically(st, log)

	go func() {
		log.Info("中继已启动", "addr", *addr, "已知开发机", st.Count(), "multi_tenant", *multi, "node", *nodeID)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("监听失败", "error", err)
			os.Exit(1)
		}
	}()

	if *adminAddr != "" {
		go func() {
			mux := http.NewServeMux()
			mux.Handle("/metrics", metrics.Handler())
			log.Info("admin 已启动", "addr", *adminAddr)
			if err := http.ListenAndServe(*adminAddr, mux); err != nil {
				log.Error("admin 监听失败", "error", err)
			}
		}()
	}

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	<-signals

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(ctx)
	_ = st.Flush()
	log.Info("中继已退出")
}

func flushPeriodically(hosts store.Store, log *slog.Logger) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		if err := hosts.Flush(); err != nil {
			log.Warn("注册信息落盘失败", "error", err)
		}
	}
}

func envOrFirst(primary, fallbackKey, defaultValue string) string {
	if value := os.Getenv(primary); value != "" {
		return value
	}
	if value := os.Getenv(fallbackKey); value != "" {
		return value
	}
	return defaultValue
}

func envOr(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func hostname() string {
	name, err := os.Hostname()
	if err != nil || name == "" {
		return "local"
	}
	return name
}

func resolveStatePath(chosen, primaryDefault, legacy string) string {
	if chosen != primaryDefault {
		return chosen
	}
	if fileExists(chosen) {
		return chosen
	}
	if fileExists(legacy) {
		return legacy
	}
	return chosen
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
