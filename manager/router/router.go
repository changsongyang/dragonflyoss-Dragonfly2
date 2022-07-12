/*
 *     Copyright 2020 The Dragonfly Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *      http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package router

import (
	"io"
	"os"
	"path/filepath"

	"github.com/casbin/casbin/v2"
	"github.com/gin-contrib/cors"
	"github.com/gin-contrib/static"
	"github.com/gin-gonic/gin"
	ginprometheus "github.com/mcuadros/go-gin-prometheus"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"

	"d7y.io/dragonfly/v2/manager/config"
	"d7y.io/dragonfly/v2/manager/handlers"
	"d7y.io/dragonfly/v2/manager/middlewares"
	"d7y.io/dragonfly/v2/manager/service"
)

const (
	PrometheusSubsystemName = "dragonfly_manager"
	OtelServiceName         = "dragonfly-manager"
)

var GinLogFileName = "gin.log"

func Init(cfg *config.Config, logDir string, service service.Service, enforcer *casbin.Enforcer) (*gin.Engine, error) {
	// Set mode
	if !cfg.Verbose {
		gin.SetMode(gin.ReleaseMode)
	}

	// Logging to a file
	if !cfg.Console {
		gin.DisableConsoleColor()
		logDir := filepath.Join(logDir, "manager")
		f, _ := os.Create(filepath.Join(logDir, GinLogFileName))
		gin.DefaultWriter = io.MultiWriter(f)
	}

	r := gin.New()
	h := handlers.New(service)

	// Prometheus metrics
	p := ginprometheus.NewPrometheus(PrometheusSubsystemName)
	p.Use(r)

	// Opentelemetry
	if cfg.Options.Telemetry.Jaeger != "" {
		r.Use(otelgin.Middleware(OtelServiceName))
	}

	// CORS
	corsConfig := cors.DefaultConfig()
	corsConfig.AllowAllOrigins = true

	// Middleware
	r.Use(gin.Logger())
	r.Use(gin.Recovery())
	r.Use(middlewares.Error())
	r.Use(cors.New(corsConfig))

	rbac := middlewares.RBAC(enforcer)
	jwt, err := middlewares.Jwt(service)
	if err != nil {
		return nil, err
	}

	// Manager View
	r.Use(static.Serve("/", static.LocalFile(cfg.Server.PublicPath, true)))

	// Router
	apiv1 := r.Group("/api/v1")

	// User
	u := apiv1.Group("/users")
	u.PATCH(":id", jwt.MiddlewareFunc(), rbac, h.UpdateUser)
	u.GET(":id", jwt.MiddlewareFunc(), rbac, h.GetUser)
	u.GET("", jwt.MiddlewareFunc(), rbac, h.GetUsers)
	u.POST("signin", jwt.LoginHandler)
	u.POST("signout", jwt.LogoutHandler)
	u.POST("signup", h.SignUp)
	u.GET("signin/:name", h.OauthSignin)
	u.GET("signin/:name/callback", h.OauthSigninCallback(jwt))
	u.POST("refresh_token", jwt.RefreshHandler)
	u.POST(":id/reset_password", h.ResetPassword)
	u.GET(":id/roles", jwt.MiddlewareFunc(), rbac, h.GetRolesForUser)
	u.PUT(":id/roles/:role", jwt.MiddlewareFunc(), rbac, h.AddRoleToUser)
	u.DELETE(":id/roles/:role", jwt.MiddlewareFunc(), rbac, h.DeleteRoleForUser)

	// Role
	re := apiv1.Group("/roles", jwt.MiddlewareFunc(), rbac)
	re.POST("", h.CreateRole)
	re.DELETE(":role", h.DestroyRole)
	re.GET(":role", h.GetRole)
	re.GET("", h.GetRoles)
	re.POST(":role/permissions", h.AddPermissionForRole)
	re.DELETE(":role/permissions", h.DeletePermissionForRole)

	// Permission
	pm := apiv1.Group("/permissions", jwt.MiddlewareFunc(), rbac)
	pm.GET("", h.GetPermissions(r))

	// Oauth
	oa := apiv1.Group("/oauth")
	oa.POST("", jwt.MiddlewareFunc(), rbac, h.CreateOauth)
	oa.DELETE(":id", jwt.MiddlewareFunc(), rbac, h.DestroyOauth)
	oa.PATCH(":id", jwt.MiddlewareFunc(), rbac, h.UpdateOauth)
	oa.GET(":id", h.GetOauth)
	oa.GET("", h.GetOauths)

	// Scheduler Cluster
	sc := apiv1.Group("/scheduler-clusters", jwt.MiddlewareFunc(), rbac)
	sc.POST("", h.CreateSchedulerCluster)
	sc.DELETE(":id", h.DestroySchedulerCluster)
	sc.PATCH(":id", h.UpdateSchedulerCluster)
	sc.GET(":id", h.GetSchedulerCluster)
	sc.GET("", h.GetSchedulerClusters)
	sc.PUT(":id/schedulers/:scheduler_id", h.AddSchedulerToSchedulerCluster)

	// Scheduler
	s := apiv1.Group("/schedulers", jwt.MiddlewareFunc(), rbac)
	s.POST("", h.CreateScheduler)
	s.DELETE(":id", h.DestroyScheduler)
	s.PATCH(":id", h.UpdateScheduler)
	s.GET(":id", h.GetScheduler)
	s.GET("", h.GetSchedulers)

	// Scheduler Model
	m := apiv1.Group("/models", jwt.MiddlewareFunc(), rbac)
	m.GET(":id", h.GetVerisonById)
	m.GET("", h.GetVersions)
	m.PATCH(":id", h.UpdateVersionById)
	m.DELETE(":id", h.DeleteVersionById)

	// Scheduler Model Version
	m.GET(":id/versions/:version_id", h.GetModelById)
	m.GET(":id/versions", h.GetModels)
	m.PATCH(":id/versions/:version_id", h.UpdateModelById)
	m.DELETE(":id/versions/:version_id", h.DeleteModelById)

	// Application
	cs := apiv1.Group("/applications", jwt.MiddlewareFunc(), rbac)
	cs.POST("", h.CreateApplication)
	cs.DELETE(":id", h.DestroyApplication)
	cs.PATCH(":id", h.UpdateApplication)
	cs.GET(":id", h.GetApplication)
	cs.GET("", h.GetApplications)
	cs.PUT(":id/scheduler-clusters/:scheduler_cluster_id", h.AddSchedulerClusterToApplication)
	cs.DELETE(":id/scheduler-clusters/:scheduler_cluster_id", h.DeleteSchedulerClusterToApplication)
	cs.PUT(":id/seed-peer-clusters/:seed_peer_cluster_id", h.AddSeedPeerClusterToApplication)
	cs.DELETE(":id/seed-peer-clusters/:seed_peer_cluster_id", h.DeleteSeedPeerClusterToApplication)

	// Seed Peer Cluster
	spc := apiv1.Group("/seed-peer-clusters", jwt.MiddlewareFunc(), rbac)
	spc.POST("", h.CreateSeedPeerCluster)
	spc.DELETE(":id", h.DestroySeedPeerCluster)
	spc.PATCH(":id", h.UpdateSeedPeerCluster)
	spc.GET(":id", h.GetSeedPeerCluster)
	spc.GET("", h.GetSeedPeerClusters)
	spc.PUT(":id/seed-peers/:seed_peer_id", h.AddSeedPeerToSeedPeerCluster)
	spc.PUT(":id/scheduler-clusters/:scheduler_cluster_id", h.AddSchedulerClusterToSeedPeerCluster)

	// Seed Peer
	sp := apiv1.Group("/seed-peers", jwt.MiddlewareFunc(), rbac)
	sp.POST("", h.CreateSeedPeer)
	sp.DELETE(":id", h.DestroySeedPeer)
	sp.PATCH(":id", h.UpdateSeedPeer)
	sp.GET(":id", h.GetSeedPeer)
	sp.GET("", h.GetSeedPeers)

	// Security Rule
	sr := apiv1.Group("/security-rules", jwt.MiddlewareFunc(), rbac)
	sr.POST("", h.CreateSecurityRule)
	sr.DELETE(":id", h.DestroySecurityRule)
	sr.PATCH(":id", h.UpdateSecurityRule)
	sr.GET(":id", h.GetSecurityRule)
	sr.GET("", h.GetSecurityRules)

	// Security Group
	sg := apiv1.Group("/security-groups", jwt.MiddlewareFunc(), rbac)
	sg.POST("", h.CreateSecurityGroup)
	sg.DELETE(":id", h.DestroySecurityGroup)
	sg.PATCH(":id", h.UpdateSecurityGroup)
	sg.GET(":id", h.GetSecurityGroup)
	sg.GET("", h.GetSecurityGroups)
	sg.PUT(":id/scheduler-clusters/:scheduler_cluster_id", h.AddSchedulerClusterToSecurityGroup)
	sg.PUT(":id/seed-peer-clusters/:seed_peer_cluster_id", h.AddSeedPeerClusterToSecurityGroup)
	sg.PUT(":id/security-rules/:security_rule_id", h.AddSecurityRuleToSecurityGroup)
	sg.DELETE(":id/security-rules/:security_rule_id", h.DestroySecurityRuleToSecurityGroup)

	// Bucket
	bucket := apiv1.Group("/buckets", jwt.MiddlewareFunc(), rbac)
	bucket.POST("", h.CreateBucket)
	bucket.DELETE(":id", h.DestroyBucket)
	bucket.GET(":id", h.GetBucket)
	bucket.GET("", h.GetBuckets)

	// Config
	config := apiv1.Group("/configs")
	config.POST("", jwt.MiddlewareFunc(), rbac, h.CreateConfig)
	config.DELETE(":id", jwt.MiddlewareFunc(), rbac, h.DestroyConfig)
	config.PATCH(":id", jwt.MiddlewareFunc(), rbac, h.UpdateConfig)
	config.GET(":id", jwt.MiddlewareFunc(), rbac, h.GetConfig)
	config.GET("", h.GetConfigs)

	// Job
	job := apiv1.Group("/jobs")
	job.POST("", h.CreateJob)
	job.DELETE(":id", h.DestroyJob)
	job.PATCH(":id", h.UpdateJob)
	job.GET(":id", h.GetJob)
	job.GET("", h.GetJobs)

	// Compatible with the V1 preheat.
	pv1 := r.Group("/preheats")
	r.GET("_ping", h.GetHealth)
	pv1.POST("", h.CreateV1Preheat)
	pv1.GET(":id", h.GetV1Preheat)

	// Health Check
	r.GET("/healthy", h.GetHealth)

	// Swagger
	apiSeagger := ginSwagger.URL("/swagger/doc.json")
	r.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler, apiSeagger))

	// Fallback To Manager View
	r.NoRoute(func(c *gin.Context) {
		c.File(filepath.Join(cfg.Server.PublicPath, "index.html"))
	})

	return r, nil
}
