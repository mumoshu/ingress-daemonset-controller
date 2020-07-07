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

package controllers

import (
	"context"
	"errors"
	"fmt"
	"io/ioutil"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	DeploymentIdentifierField     = "deploymentIdentifier"
	DeploymentOwnerField          = "deploymentOwner"
	DaemonSetOwnerField           = "daemonSetOwner"
	HealthCheckNodePortAnnotation = "ingressdaemonsets.mumoshu.github.io/health-check-node-port"

	finalizerName = "ingressdaemonsets.mumoshu.github.io"
)

var (
	defaultRequeueAfterOnError = time.Second * 10
)

// DeploymentReconciler reconciles a Deployment object
type DeploymentReconciler struct {
	client.Client
	Log    logr.Logger
	Scheme *runtime.Scheme

	IngressDaemonSetName string
	DeploymentName       string

	Namespace string

	lastObservedMutex *sync.Mutex
	lastObserved      *appsv1.Deployment
}

// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete

func (r *DeploymentReconciler) Reconcile(req ctrl.Request) (ctrl.Result, error) {
	ctx := context.Background()
	log := r.Log.WithValues("deployment", req.NamespacedName)

	name := req.Name
	ns := req.Namespace

	if name != r.DeploymentName || ns != r.Namespace {
		return ctrl.Result{}, nil
	}

	var deploy appsv1.Deployment

	if err := r.Get(ctx, req.NamespacedName, &deploy); err != nil {
		if kerrors.IsNotFound(err) {
			r.lastObservedMutex.Lock()
			r.lastObserved = nil
			r.lastObservedMutex.Unlock()

			log.Error(err, "healthcheck server will start failing checks due to that no deployment found")

			return ctrl.Result{}, err
		}

		log.Error(err, "retrying in a while")

		return ctrl.Result{RequeueAfter: defaultRequeueAfterOnError}, err
	}

	r.lastObservedMutex.Lock()
	defer r.lastObservedMutex.Unlock()
	r.lastObserved = &deploy

	return ctrl.Result{}, nil
}

func (r *DeploymentReconciler) generateHttpResponse() *http.Response {
	observed := r.lastObserved

	if observed == nil {
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Body:       ioutil.NopCloser(strings.NewReader("This node is unhealthy because no per-node deployment and ready pods are available on it")),
		}
	}

	replicas := observed.Spec.Replicas
	availables := observed.Status.AvailableReplicas
	if replicas != nil && availables >= *replicas {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       ioutil.NopCloser(strings.NewReader(fmt.Sprintf("This node is healthy because %d / %d replicas are available", availables, *replicas))),
		}
	}

	return &http.Response{
		StatusCode: http.StatusServiceUnavailable,
		Body:       ioutil.NopCloser(strings.NewReader(fmt.Sprintf("This node is unhealthy because only %d / %d replicas are available", availables, *replicas))),
	}
}

func (r *DeploymentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.lastObservedMutex = &sync.Mutex{}

	if r.DeploymentName == "" {
		return errors.New("validating deployment name: it must be set to a non-empty string")
	}

	if r.Namespace == "" {
		return errors.New("validating namespace: it must be set to a non-empty string")
	}

	if err := mgr.GetFieldIndexer().IndexField(&appsv1.Deployment{}, DeploymentOwnerField, func(rawObj runtime.Object) []string {
		deploy := rawObj.(*appsv1.Deployment)

		owner := metav1.GetControllerOf(deploy)
		if owner == nil {
			return nil
		}

		if deploy.Namespace != r.Namespace {
			return nil
		}

		if owner.Kind == "IngressDaemonSet" {
			if r.IngressDaemonSetName == "" || owner.Name == r.IngressDaemonSetName {
				return []string{owner.Name}
			}
			return nil
		}

		return []string{}
	}); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&appsv1.Deployment{}).
		Complete(r)
}

func (r *DeploymentReconciler) Handler() http.Handler {
	handler := http.NewServeMux()
	handler.HandleFunc("/ok", func(w http.ResponseWriter, req *http.Request) {
		res := r.generateHttpResponse()

		w.WriteHeader(res.StatusCode)
		body, err := ioutil.ReadAll(res.Body)
		if err != nil {
			panic(err)
		}

		if _, err := w.Write(body); err != nil {
			panic(err)
		}
	})
	return handler
}
