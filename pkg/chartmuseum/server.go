package chartmuseum

import (
	"fmt"
	"regexp"
	"sync"

	"github.com/kubernetes-helm/chartmuseum/pkg/repo"
	"github.com/kubernetes-helm/chartmuseum/pkg/storage"

	"github.com/atarantini/ginrequestid"
	"github.com/gin-contrib/location"
	"github.com/gin-gonic/gin"
	"github.com/zsais/go-gin-prometheus"
)

type (
	// Router handles all incoming HTTP requests
	Router struct {
		*gin.Engine
	}

	// Server contains a Logger, Router, storage backend and object cache
	Server struct {
		Logger                  *Logger
		Router                  *Router
		RepositoryIndex         *repo.Index
		StorageBackend          storage.Backend
		StorageCache            []storage.Object
		AllowOverwrite          bool
		MultiTenancyEnabled     bool
		AnonymousGet            bool
		TlsCert                 string
		TlsKey                  string
		ChartPostFormFieldName  string
		ProvPostFormFieldName   string
		regenerationLock        *sync.Mutex
		fetchedObjectsLock      *sync.Mutex
		fetchedObjectsChans     []chan fetchedObjects
		regeneratedIndexesChans []chan indexRegeneration
	}

	// ServerOptions are options for constructing a Server
	ServerOptions struct {
		StorageBackend         storage.Backend
		LogJSON                bool
		Debug                  bool
		EnableAPI              bool
		AllowOverwrite         bool
		EnableMetrics          bool
		EnableMultiTenancy     bool
		AnonymousGet           bool
		ChartURL               string
		TlsCert                string
		TlsKey                 string
		Username               string
		Password               string
		ChartPostFormFieldName string
		ProvPostFormFieldName  string
	}
)

// NewRouter creates a new Router instance
func NewRouter(logger *Logger, enableMetrics bool) *Router {
	gin.SetMode(gin.ReleaseMode)
	engine := gin.New()
	engine.Use(location.Default(), ginrequestid.RequestId(), loggingMiddleware(logger), gin.Recovery())
	if enableMetrics {
		p := ginprometheus.NewPrometheus("chartmuseum")
		p.ReqCntURLLabelMappingFn = mapURLWithParamsBackToRouteTemplate
		p.Use(engine)
	}
	return &Router{engine}
}

// NewServer creates a new Server instance
func NewServer(options ServerOptions) (*Server, error) {
	logger, err := NewLogger(options.LogJSON, options.Debug)
	if err != nil {
		return new(Server), nil
	}

	router := NewRouter(logger, options.EnableMetrics)

	server := &Server{
		Logger:                 logger,
		Router:                 router,
		RepositoryIndex:        repo.NewIndex(options.ChartURL),
		StorageBackend:         options.StorageBackend,
		StorageCache:           []storage.Object{},
		AllowOverwrite:         options.AllowOverwrite,
		MultiTenancyEnabled:    options.EnableMultiTenancy,
		AnonymousGet:           options.AnonymousGet,
		TlsCert:                options.TlsCert,
		TlsKey:                 options.TlsKey,
		ChartPostFormFieldName: options.ChartPostFormFieldName,
		ProvPostFormFieldName:  options.ProvPostFormFieldName,
		regenerationLock:       &sync.Mutex{},
		fetchedObjectsLock:     &sync.Mutex{},
	}

	if server.MultiTenancyEnabled {
		logger.Debugw("Multi-Tenancy Enabled")
		server.setMultiTenancyRoutes()
	} else {
		server.setRoutes(options.Username, options.Password, options.EnableAPI)
		log := server.contextLoggingFn(&gin.Context{})
		_, err = server.syncRepositoryIndex(log) // prime the cache
	}
	return server, err
}

// Listen starts server on a given port
func (server *Server) Listen(port int) {
	server.Logger.Infow("Starting ChartMuseum",
		"port", port,
	)
	if server.TlsCert != "" && server.TlsKey != "" {
		server.Logger.Fatal(server.Router.RunTLS(fmt.Sprintf(":%d", port), server.TlsCert, server.TlsKey))
	} else {
		server.Logger.Fatal(server.Router.Run(fmt.Sprintf(":%d", port)))
	}
}

/*
mapURLWithParamsBackToRouteTemplate is a valid ginprometheus ReqCntURLLabelMappingFn.
For every route containing parameters (e.g. `/charts/:filename`, `/api/charts/:name/:version`, etc)
the actual parameter values will be replaced by their name, to minimize the cardinality of the
`chartmuseum_requests_total{url=..}` Prometheus counter.
*/
func mapURLWithParamsBackToRouteTemplate(c *gin.Context) string {
	url := c.Request.URL.String()
	for _, p := range c.Params {
		re := regexp.MustCompile(fmt.Sprintf(`(^.*?)/\b%s\b(.*$)`, regexp.QuoteMeta(p.Value)))
		url = re.ReplaceAllString(url, fmt.Sprintf(`$1/:%s$2`, p.Key))
	}
	return url
}
