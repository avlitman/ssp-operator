package validator

import (
	"fmt"
	"net/http"
	"os"

	templatev1 "github.com/openshift/api/template/v1"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	flag "github.com/spf13/pflag"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	kubevirtv1 "kubevirt.io/api/core/v1"

	"kubevirt.io/ssp-operator/internal/template-validator/logger"
	"kubevirt.io/ssp-operator/internal/template-validator/service"
	"kubevirt.io/ssp-operator/internal/template-validator/tlsinfo"
	"kubevirt.io/ssp-operator/internal/template-validator/version"
	"kubevirt.io/ssp-operator/internal/template-validator/virtinformers"
	validating "kubevirt.io/ssp-operator/internal/template-validator/webhooks"
	validatorMetrics "kubevirt.io/ssp-operator/pkg/monitoring/metrics/template-validator"
)

const (
	defaultPort = 8443
	defaultHost = "0.0.0.0"
)

type App struct {
	service.ServiceListen
	TLSInfo     tlsinfo.TLSInfo
	versionOnly bool
}

var _ service.Service = &App{}

func (app *App) AddFlags() {
	app.InitFlags()
	app.BindAddress = defaultHost
	app.Port = defaultPort
	app.AddCommonFlags()

	flag.StringVarP(&app.TLSInfo.CertsDirectory, "cert-dir", "c", "", "specify path to the directory containing TLS key and certificate - this enables TLS")
	flag.BoolVarP(&app.versionOnly, "version", "V", false, "show version and exit")
}

func (app *App) Run() {
	logger.Log.Info("Starting",
		"component", version.COMPONENT,
		"version", version.VERSION,
		"revision", version.REVISION,
	)
	if app.versionOnly {
		return
	}

	app.TLSInfo.Init()
	defer app.TLSInfo.Clean()

	// We cannot use default scheme.Scheme, because it contains duplicate definitions
	// for kubevirt resources and the client would fail with an error:
	// "multiple group-version-kinds associated with type *v1.VirtualMachineList, refusing to guess at one"
	apiScheme := createScheme()

	informers, err := virtinformers.NewInformers(apiScheme)
	if err != nil {
		logger.Log.Error(err, "Error creating informers")
		panic(err)
	}

	informers.Start()
	defer informers.Stop()

	validating.NewWebhooks(informers).Register()

	registerReadinessProbe()

	// setup monitoring
	validatorMetrics.SetupMetrics()

	logger.Log.Info("TLS certs directory", "directory", app.TLSInfo.CertsDirectory)

	http.Handle("/metrics", promhttp.Handler())

	if app.TLSInfo.IsEnabled() {
		server := &http.Server{Addr: app.Address(), TLSConfig: app.TLSInfo.CreateTlsConfig()}
		logger.Log.Info("TLS configured, serving over HTTPS", "address", app.Address())
		if err := server.ListenAndServeTLS("", ""); err != nil {
			logger.Log.Error(err, "Error listening TLS")
			panic(err)
		}
	} else {
		logger.Log.Info("TLS disabled, serving over HTTP", "address", app.Address())
		if err := http.ListenAndServe(app.Address(), nil); err != nil {
			logger.Log.Error(err, "Error listening")
			panic(err)
		}
	}
}

func registerReadinessProbe() {
	http.HandleFunc("/readyz", func(resp http.ResponseWriter, req *http.Request) {
		fmt.Fprintf(resp, "ok")
	})
}

func createScheme() *runtime.Scheme {
	sch := runtime.NewScheme()

	utilruntime.Must(clientgoscheme.AddToScheme(sch))
	utilruntime.Must(templatev1.Install(sch))

	// Setting API version of kubevirt that we want to register
	utilruntime.Must(os.Setenv(kubevirtv1.KubeVirtClientGoSchemeRegistrationVersionEnvVar, "v1"))
	utilruntime.Must(kubevirtv1.AddToScheme(sch))

	return sch
}
