/*

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"context"
	"flag"
	"github.com/mumoshu/ingress-daemonset-controller/controllers"
	"golang.org/x/sync/errgroup"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
	"net/http"
	"os"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"time"
	// +kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	_ = clientgoscheme.AddToScheme(scheme)

	// +kubebuilder:scaffold:scheme
}

func main() {
	var metricsAddr string
	var ingressDaemonSetName string
	var deploymentName string
	var namespace string
	var healthCheckAddr string
	var enableLeaderElection bool
	var syncPeriod time.Duration

	flag.StringVar(&metricsAddr, "metrics-addr", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&namespace, "namespace", "", "The namespace that the ingress-daemonset and deployment reside in")
	flag.StringVar(&ingressDaemonSetName, "ingress-daemonset", "", "The ingress daemonset's name to look for the target deployment")
	flag.StringVar(&deploymentName, "deployment", "", "The deployment's name to watch for")
	flag.StringVar(&healthCheckAddr, "healthcheck-addr", ":8081", "The address the health-check endpoint binds to.")
	flag.DurationVar(&syncPeriod, "sync-period", 5*time.Second, "The interval to (re)sync the deployment's state to the health-check status")
	flag.BoolVar(&enableLeaderElection, "enable-leader-election", false,
		"Enable leader election for controller manager. Enabling this will ensure there is only one active controller manager.")
	flag.Parse()

	ctrl.SetLogger(zap.New(func(o *zap.Options) {
		o.Development = true
	}))

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:             scheme,
		MetricsBindAddress: metricsAddr,
		Namespace:          namespace,
		Port:               9443,
		SyncPeriod:         &syncPeriod,
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	cntrl := &controllers.DeploymentReconciler{
		Client:               mgr.GetClient(),
		Log:                  ctrl.Log.WithName("controllers").WithName("Deployment"),
		Scheme:               mgr.GetScheme(),
		Namespace:            namespace,
		IngressDaemonSetName: ingressDaemonSetName,
		DeploymentName:       deploymentName,
	}
	if err = cntrl.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Deployment")
		os.Exit(1)
	}

	// +kubebuilder:scaffold:builder

	s := &http.Server{
		Addr:           healthCheckAddr,
		Handler:        cntrl.Handler(),
		ReadTimeout:    10 * time.Second,
		WriteTimeout:   10 * time.Second,
		MaxHeaderBytes: 1 << 20,
	}

	ctx2, cancel := context.WithCancel(context.Background())
	eg, ctx := errgroup.WithContext(ctx2)

	eg.Go(func() error {
		defer cancel()

		go func() {
			<-ctx.Done()

			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			setupLog.Info("http server being stopped")

			s.Shutdown(shutdownCtx)
		}()

		if err := s.ListenAndServe(); err != http.ErrServerClosed {
			setupLog.Error(err, "problem serving http")

			return err
		} else {
			setupLog.Info("http server stopped")
		}

		return nil
	})

	eg.Go(func() error {
		defer cancel()

		setupLog.Info("starting manager")
		err := mgr.Start(ctx.Done())
		if err != nil {
			setupLog.Error(err, "problem running manager")
		} else {
			setupLog.Info("manager stopped")
		}

		return err
	})

	eg.Go(func() error {
		defer cancel()

		<-ctrl.SetupSignalHandler()
		setupLog.Info("shutdown signal received. cancelling all the components")

		return nil
	})

	if err := eg.Wait(); err != nil {
		setupLog.Error(err, "problem running node-healtcheck-server")

		os.Exit(1)
	}
}
