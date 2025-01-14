package server

import (
	"github.com/loft-sh/vcluster/cmd/vcluster/context"
	"github.com/loft-sh/vcluster/pkg/authentication/delegatingauthenticator"
	"github.com/loft-sh/vcluster/pkg/authorization/allowall"
	"github.com/loft-sh/vcluster/pkg/authorization/delegatingauthorizer"
	"github.com/loft-sh/vcluster/pkg/authorization/impersonationauthorizer"
	"github.com/loft-sh/vcluster/pkg/server/filters"
	"github.com/loft-sh/vcluster/pkg/server/handler"
	"github.com/loft-sh/vcluster/pkg/util/serverhelper"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/util/sets"
	unionauthentication "k8s.io/apiserver/pkg/authentication/request/union"
	"k8s.io/apiserver/pkg/authorization/union"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/server"
	"k8s.io/apiserver/pkg/server/options"
	"k8s.io/klog"
	"net"
	"net/http"
	ctrl "sigs.k8s.io/controller-runtime"
	"strconv"
)

// Server is a http.Handler which proxies Kubernetes APIs to remote API server.
type Server struct {
	virtualManager ctrl.Manager
	handler        *http.ServeMux

	redirectResources   []delegatingauthorizer.GroupVersionResourceVerb
	requestHeaderCaFile string
	clientCaFile        string
}

// NewServer creates and installs a new Server.
// 'filter', if non-nil, protects requests to the api only.
func NewServer(ctx *context.ControllerContext, requestHeaderCaFile, clientCaFile string) (*Server, error) {
	s := &Server{
		virtualManager: ctx.VirtualManager,
		handler:        http.NewServeMux(),

		requestHeaderCaFile: requestHeaderCaFile,
		clientCaFile:        clientCaFile,
		redirectResources: []delegatingauthorizer.GroupVersionResourceVerb{
			{
				GroupVersionResource: corev1.SchemeGroupVersion.WithResource("nodes"),
				Verb:                 "*",
				SubResource:          "proxy",
			},
			{
				GroupVersionResource: corev1.SchemeGroupVersion.WithResource("pods"),
				Verb:                 "*",
				SubResource:          "portforward",
			},
			{
				GroupVersionResource: corev1.SchemeGroupVersion.WithResource("pods"),
				Verb:                 "*",
				SubResource:          "exec",
			},
			{
				GroupVersionResource: corev1.SchemeGroupVersion.WithResource("pods"),
				Verb:                 "*",
				SubResource:          "attach",
			},
			{
				GroupVersionResource: corev1.SchemeGroupVersion.WithResource("pods"),
				Verb:                 "*",
				SubResource:          "log",
			},
			{
				GroupVersionResource: corev1.SchemeGroupVersion.WithResource("pods"),
				Verb:                 "*",
				SubResource:          "proxy",
			},
			{
				GroupVersionResource: corev1.SchemeGroupVersion.WithResource("services"),
				Verb:                 "*",
				SubResource:          "proxy",
			},
		},
	}

	h := handler.ImpersonatingHandler("", ctx.VirtualManager.GetConfig())
	h = filters.WithServiceCreateRedirect(h, ctx.LocalManager, ctx.VirtualManager, ctx.Options.TargetNamespace, ctx.LockFactory.GetLock("service-controller"))
	h = filters.WithRedirect(h, ctx.LocalManager, ctx.Options.TargetNamespace, s.redirectResources)
	h = filters.WithMetricsRewrite(h, ctx.LocalManager, ctx.VirtualManager, ctx.Options.TargetNamespace)
	h = filters.WithInjectedMetrics(h, ctx.LocalManager, ctx.VirtualManager, ctx.Options.TargetNamespace)
	serverhelper.HandleRoute(s.handler, "/", h)

	return s, nil
}

// ServeOnListenerTLS starts the server using given listener with TLS, loops forever until an error occurs
func (s *Server) ServeOnListenerTLS(address string, port int, certFile, keyFile string, stopChan <-chan struct{}) error {
	// kubernetes build handler configuration
	serverConfig := server.NewConfig(serializer.NewCodecFactory(s.virtualManager.GetScheme()))
	serverConfig.RequestInfoResolver = &request.RequestInfoFactory{
		APIPrefixes:          sets.NewString("api", "apis"),
		GrouplessAPIPrefixes: sets.NewString("api"),
	}

	redirectAuthResources := []delegatingauthorizer.GroupVersionResourceVerb{
		{
			GroupVersionResource: corev1.SchemeGroupVersion.WithResource("services"),
			Verb:                 "create",
			SubResource:          "",
		},
	}
	redirectAuthResources = append(redirectAuthResources, s.redirectResources...)
	serverConfig.Authorization.Authorizer = union.New(delegatingauthorizer.New(s.virtualManager, redirectAuthResources, []delegatingauthorizer.PathVerb{
		{
			Path: "/metrics/cadvisor",
			Verb: "*",
		},
		{
			Path: "/metrics/probes",
			Verb: "*",
		},
		{
			Path: "/metrics/resource",
			Verb: "*",
		},
		{
			Path: "/metrics/resource/v1alpha1",
			Verb: "*",
		},
	}), impersonationauthorizer.New(s.virtualManager.GetClient()), allowall.New())

	sso := options.NewSecureServingOptions()
	sso.HTTP2MaxStreamsPerConnection = 1000
	sso.ServerCert.CertKey.CertFile = certFile
	sso.ServerCert.CertKey.KeyFile = keyFile
	sso.BindPort = port
	sso.BindAddress = net.ParseIP(address)
	err := sso.WithLoopback().ApplyTo(&serverConfig.SecureServing, &serverConfig.LoopbackClientConfig)
	if err != nil {
		return err
	}

	authOptions := options.NewDelegatingAuthenticationOptions()
	authOptions.RemoteKubeConfigFileOptional = true
	authOptions.SkipInClusterLookup = true
	authOptions.RequestHeader.ClientCAFile = s.requestHeaderCaFile
	authOptions.ClientCert.ClientCA = s.clientCaFile
	err = authOptions.ApplyTo(&serverConfig.Authentication, serverConfig.SecureServing, serverConfig.OpenAPIConfig)
	if err != nil {
		return err
	}

	// make sure the tokens are correctly authenticated
	serverConfig.Authentication.Authenticator = unionauthentication.New(delegatingauthenticator.New(s.virtualManager.GetClient()), serverConfig.Authentication.Authenticator)

	// create server
	klog.Info("Starting tls proxy server at " + address + ":" + strconv.Itoa(port))
	stopped, err := serverConfig.SecureServing.Serve(server.DefaultBuildHandlerChain(s.handler, serverConfig), serverConfig.RequestTimeout, stopChan)
	if err != nil {
		return err
	}

	<-stopped
	return nil
}
