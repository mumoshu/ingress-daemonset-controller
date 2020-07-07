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
	"fmt"
	"github.com/davecgh/go-spew/spew"
	"hash/fnv"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/rand"
	"strconv"
	"time"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	ingressdaemonsetsv1alpha1 "github.com/mumoshu/ingress-daemonset-controller/api/v1alpha1"
)

const (
	ObjectHashAnnotationKey = "object-hash"

	DeploymentIdentifierField     = "deploymentIdentifier"
	DeploymentOwnerField          = "deploymentOwner"
	DaemonSetOwnerField           = "daemonSetOwner"
	HealthCheckNodePortAnnotation = "ingressdaemonsets.mumoshu.github.io/health-check-node-port"

	finalizerName = "ingressdaemonsets.mumoshu.github.io"
)

var (
	defaultRequeueAfterOnError = time.Second * 10
)

// IngressDaemonSetReconciler reconciles a IngressDaemonSet object
type IngressDaemonSetReconciler struct {
	client.Client
	Log    logr.Logger
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=ingressdaemonsets.mumoshu.github.io,resources=ingressdaemonsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=daemonsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=nodes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ingressdaemonsets.mumoshu.github.io,resources=ingressdaemonsets/status,verbs=get;update;patch

func (r *IngressDaemonSetReconciler) Reconcile(req ctrl.Request) (ctrl.Result, error) {
	ctx := context.Background()
	log := r.Log.WithValues("ingressdaemonset", req.NamespacedName)

	name := req.Name
	ns := req.Namespace

	var deleted bool

	var ing ingressdaemonsetsv1alpha1.IngressDaemonSet

	if err := r.Get(ctx, req.NamespacedName, &ing); err != nil {
		if !errors.IsNotFound(err) {
			return ctrl.Result{}, err
		}

		deleted = true
	}

	if !deleted && ing.GetDeletionTimestamp().IsZero() {
		if finalizers, exists := addFinalizer(ing.GetFinalizers()); !exists {
			newIng := ing.DeepCopy()
			newIng.SetFinalizers(finalizers)

			if err := r.Update(ctx, newIng); err != nil {
				log.Error(err, "Adding finalizer")

				return ctrl.Result{RequeueAfter: defaultRequeueAfterOnError}, err
			}

			return ctrl.Result{Requeue: true}, nil
		}
	}

	if ing.GetDeletionTimestamp() != nil {
		deleted = true
	}

	var ownedDaemonSetsList appsv1.DaemonSetList

	if err := r.List(ctx, &ownedDaemonSetsList, client.InNamespace(ns), client.MatchingFields{DaemonSetOwnerField: name}); err != nil {
		return ctrl.Result{}, err
	}

	boundHealthCheckNodePorts := map[int]bool{}

	for _, d := range ownedDaemonSetsList.Items {
		p, err := GetHealthCheckNodePort(d)
		if err != nil {
			return ctrl.Result{RequeueAfter: defaultRequeueAfterOnError}, err
		}

		boundHealthCheckNodePorts[p] = true
	}

	var nodeList corev1.NodeList

	targetedNodeNames := map[string]bool{}

	var opts []client.ListOption

	if len(ing.Spec.NodeSelector) > 0 {
		opts = append(opts, client.MatchingLabels(ing.Spec.NodeSelector))
	}

	if err := r.List(ctx, &nodeList, opts...); err != nil {
		return ctrl.Result{}, err
	}

	for _, n := range nodeList.Items {
		targetedNodeNames[n.Name] = true
	}

	var ownedDeploysList appsv1.DeploymentList

	if err := r.List(ctx, &ownedDeploysList, client.InNamespace(ns), client.MatchingFields{DeploymentOwnerField: name}); err != nil {
		return ctrl.Result{}, err
	}

	if deleted {
		if len(ownedDaemonSetsList.Items) == 0 && len(ownedDeploysList.Items) == 0 {
			if finalizers, removed := removeFinalizer(ing.Finalizers); removed {
				newIng := ing.DeepCopy()
				newIng.SetFinalizers(finalizers)

				if err := r.Update(ctx, newIng); err != nil {
					log.Error(err, "Removing finalizer")

					return ctrl.Result{RequeueAfter: defaultRequeueAfterOnError}, err
				}
			}

			return ctrl.Result{}, nil
		}

		for _, o := range ownedDaemonSetsList.Items {
			if o.GetDeletionTimestamp() != nil {
				continue
			}

			if err := r.Delete(ctx, &o); err != nil {
				log.Error(err, "Failed to delete daemonset")

				return ctrl.Result{RequeueAfter: defaultRequeueAfterOnError}, err
			}
		}

		for _, o := range ownedDeploysList.Items {
			if o.GetDeletionTimestamp() != nil {
				continue
			}

			if err := r.Delete(ctx, &o); err != nil {
				log.Error(err, "Failed to delete deployment")

				return ctrl.Result{RequeueAfter: defaultRequeueAfterOnError}, err
			}
		}

		return ctrl.Result{}, nil
	}

	if res, err := r.reconcileDaemonSets(ctx, log, ownedDaemonSetsList, ing); res != nil {
		return *res, err
	}

	nodes := map[string]corev1.Node{}

	for _, n := range nodeList.Items {
		nodes[n.Name] = n
	}

	if res, err := r.reconcileDeployments(ctx, log, ownedDeploysList, nodes, targetedNodeNames, ing); res != nil {
		return *res, err
	}

	return ctrl.Result{}, nil
}

func (r *IngressDaemonSetReconciler) reconcileDeployments(
	ctx context.Context,
	log logr.Logger,
	ownedDeploysList appsv1.DeploymentList,
	nodes map[string]corev1.Node,
	targetedNodeNames map[string]bool,
	ing ingressdaemonsetsv1alpha1.IngressDaemonSet,
) (*ctrl.Result, error) {
	var maxDegradedNodes *int
	if ing.Spec.UpdateStrategy.RollingUpdate.MaxDegradedNodes != nil {
		maxDegradedNodes = ing.Spec.UpdateStrategy.RollingUpdate.MaxDegradedNodes
	}

	var maxUnavailableNodes *int
	if ing.Spec.UpdateStrategy.RollingUpdate.MaxUnavailableNodes != nil {
		maxUnavailableNodes = ing.Spec.UpdateStrategy.RollingUpdate.MaxUnavailableNodes
	} else {
		defaultMaxDegradedNodes := 1

		maxDegradedNodes = &defaultMaxDegradedNodes
	}

	if maxUnavailableNodes != nil {
		var numUnavailableNodes int

		for _, currentDeploy := range ownedDeploysList.Items {
			var isUpToDate bool

			desiredReplicas := *currentDeploy.Spec.Replicas

			if currentDeploy.Status.ObservedGeneration == currentDeploy.Generation &&
				currentDeploy.Status.UpdatedReplicas > desiredReplicas &&
				currentDeploy.Status.AvailableReplicas >= desiredReplicas {

				isUpToDate = true
			}

			nodeName := currentDeploy.Spec.Template.Spec.NodeName

			node := nodes[nodeName]

			isUnavailable, err := isUnavailable(ing, node)
			if err != nil {
				log.Error(err, "checking availability of node", "nodeName", node.Name)
			}

			if isUnavailable {
				numUnavailableNodes++
			}

			if maxUnavailableNodes != nil {
				if numUnavailableNodes > *maxUnavailableNodes {
					log.Info(
						"The number of unavailable(detached) nodes reached the max threshold. "+
							"Preventing more concurrent updates to happen and retrying later",
						"numUnavailableNodes", numUnavailableNodes,
						"maxNumUnavailableNodes", *maxUnavailableNodes,
					)

					return &ctrl.Result{RequeueAfter: defaultRequeueAfterOnError}, nil
				}
			}

			desiredDeploy := r.newDeployment(ing, nodeName)

			oldHash := currentDeploy.Annotations[ObjectHashAnnotationKey]
			newHash := desiredDeploy.Annotations[ObjectHashAnnotationKey]

			if newHash != oldHash {
				if !isUnavailable {
					// Annotate per-node deployment
					{
						// If not annotated yet, annotate the node to detach and wait until the detachment timestamp is set
						// and is older than the current time minus the grace period
						deployDetachmentAnnotationKey := "ingressdaemonsets.mumoshu.github.com/to-be-updated"
						if ing.Spec.UpdateStrategy.RollingUpdate.AnnotateDeploymentToDetach != nil && ing.Spec.UpdateStrategy.RollingUpdate.AnnotateDeploymentToDetach.Key != "" {
							deployDetachmentAnnotationKey = ing.Spec.UpdateStrategy.RollingUpdate.AnnotateDeploymentToDetach.Key
						}

						var detachmentAnnotationValue string
						if ing.Spec.UpdateStrategy.RollingUpdate.AnnotateDeploymentToDetach != nil {
							detachmentAnnotationValue = *ing.Spec.UpdateStrategy.RollingUpdate.AnnotateDeploymentToDetach.Value
						}

						if as, updated := SetAnnotation(currentDeploy.GetObjectMeta(), deployDetachmentAnnotationKey, detachmentAnnotationValue); updated {
							newDeploy := currentDeploy.DeepCopy()
							newDeploy.SetAnnotations(as)

							if err := r.Update(ctx, newDeploy); err != nil {
								log.Error(err, "Annotating deployment")

								return &ctrl.Result{RequeueAfter: defaultRequeueAfterOnError}, err
							}

							return &ctrl.Result{Requeue: true}, nil
						}
					}

					// Annotate node
					{
						// If not annotated yet, annotate the node to detach and wait until the detachment timestamp is set
						// and is older than the current time minus the grace period
						nodeDetachmentAnnotationKey := getDetachNodeAnnotationKey(ing)

						var detachmentAnnotationValue string
						if v := getDetachNodeAnnotationValue(ing); v != nil {
							detachmentAnnotationValue = *v
						}

						if as, updated := SetAnnotation(node.GetObjectMeta(), nodeDetachmentAnnotationKey, detachmentAnnotationValue); updated {
							newNode := node.DeepCopy()
							newNode.SetAnnotations(as)

							if err := r.Update(ctx, newNode); err != nil {
								log.Error(err, "Annotating node")

								return &ctrl.Result{RequeueAfter: defaultRequeueAfterOnError}, err
							}

							return &ctrl.Result{Requeue: true}, nil
						}
					}
				}

				if key := getDetachNodeTimestampAnnotationKey(ing); key != "" {
					rs, ok := GetAnnotation(currentDeploy.GetObjectMeta(), key)

					if !ok || rs == "" {
						log.Info("Waiting until detachment timestamp annotation is set")

						numUnavailableNodes++

						continue
					}

					if format := ing.Spec.UpdateStrategy.RollingUpdate.WaitForDetachmentByAnnotatedTimestamp.Format; format != nil && *format != "RFC3339" {
						return &ctrl.Result{}, fmt.Errorf("unsupported timestamp annotation format: %s", *format)
					}

					t, err := time.Parse(time.RFC3339, rs)
					if err != nil {
						return &ctrl.Result{}, fmt.Errorf("parsing detachment timestamp value %q: %w", rs, err)
					}

					gracePeriod := time.Second * 10

					if sec := ing.Spec.UpdateStrategy.RollingUpdate.WaitForDetachmentByAnnotatedTimestamp.GracePeriodSeconds; sec > 0 {
						gracePeriod = time.Second * time.Duration(sec)
					}

					now := time.Now()

					until := t.Add(gracePeriod)
					if now.Before(until) {
						remaining := until.Sub(now)

						log.Info("Waiting until remaining detachment grace period passes", "now", t, "until", until, "remaining", remaining)

						continue
					}
				}

				newDS := currentDeploy.DeepCopy()
				newDS.Spec = desiredDeploy.Spec

				if err := r.Update(ctx, newDS); err != nil {
					log.Error(err, "Updating deployment")

					return &ctrl.Result{RequeueAfter: defaultRequeueAfterOnError}, err
				}

				return &ctrl.Result{Requeue: true}, nil
			} else if isUnavailable && isUpToDate {
				// Remove the detachment annotation to attach the node again
				detachKey := getDetachNodeAnnotationKey(ing)

				annotations := map[string]string{}

				for k, v := range ing.Annotations {
					if k == detachKey {
						continue
					}

					annotations[k] = v
				}

				newDeploy := currentDeploy.DeepCopy()
				newDeploy.Annotations = annotations

				if err := r.Update(ctx, newDeploy); err != nil {
					log.Error(err, "Updating deployment")

					return &ctrl.Result{RequeueAfter: defaultRequeueAfterOnError}, err
				}

				return &ctrl.Result{Requeue: true}, nil
			}

			delete(targetedNodeNames, nodeName)
		}
	} else {
		var numDegradedNodes int

		for _, currentDeploy := range ownedDeploysList.Items {
			if maxDegradedNodes != nil {
				if currentDeploy.Status.ObservedGeneration != currentDeploy.Generation || currentDeploy.Status.UpdatedReplicas < currentDeploy.Status.AvailableReplicas {
					numDegradedNodes++
				}

				if numDegradedNodes >= *maxDegradedNodes {
					log.Info(
						"The number of degraded nodes reached the max threshold. "+
							"Preventing more concurrent updates to happen and retrying later",
						"numDegradedNode", numDegradedNodes,
						"maxNumDegradedNodes", *maxDegradedNodes,
					)

					return &ctrl.Result{RequeueAfter: defaultRequeueAfterOnError}, nil
				}
			}

			nodeName := currentDeploy.Spec.Template.Spec.NodeName

			if !targetedNodeNames[nodeName] {
				if err := r.Delete(ctx, &currentDeploy); err != nil {
					log.Error(err, "Failed to delete deployment")

					return &ctrl.Result{RequeueAfter: defaultRequeueAfterOnError}, err
				}
			}

			desiredDeploy := r.newDeployment(ing, nodeName)

			oldHash := currentDeploy.Annotations[ObjectHashAnnotationKey]
			newHash := desiredDeploy.Annotations[ObjectHashAnnotationKey]

			if newHash != oldHash {
				newDS := currentDeploy.DeepCopy()
				newDS.Spec = desiredDeploy.Spec

				if err := r.Update(ctx, newDS); err != nil {
					log.Error(err, "Updating deployment")

					return &ctrl.Result{RequeueAfter: defaultRequeueAfterOnError}, err
				}

				return &ctrl.Result{Requeue: true}, nil
			}

			delete(targetedNodeNames, nodeName)
		}
	}

	for nodeName := range targetedNodeNames {
		d := r.newDeployment(ing, nodeName)

		if err := r.Create(ctx, &d); err != nil {
			log.Error(err, "Creating deployment")

			return &ctrl.Result{RequeueAfter: defaultRequeueAfterOnError}, err
		}
	}

	return nil, nil
}

func getDetachNodeTimestampAnnotationKey(ing ingressdaemonsetsv1alpha1.IngressDaemonSet) string {
	v := ing.Spec.UpdateStrategy.RollingUpdate.WaitForDetachmentByAnnotatedTimestamp

	if v != nil {
		return v.Key
	}

	return ""
}

func getDetachNodeAnnotationKey(ing ingressdaemonsetsv1alpha1.IngressDaemonSet) string {
	v := ing.Spec.UpdateStrategy.RollingUpdate.AnnotateNodeToDetach

	if v != nil {
		return v.Key
	}

	return ""
}

func getDetachNodeAnnotationValue(ing ingressdaemonsetsv1alpha1.IngressDaemonSet) *string {
	v := ing.Spec.UpdateStrategy.RollingUpdate.AnnotateNodeToDetach
	if v != nil {
		return v.Value
	}
	return nil
}

func isUnavailable(ing ingressdaemonsetsv1alpha1.IngressDaemonSet, node corev1.Node) (bool, error) {
	key := getDetachNodeAnnotationKey(ing)
	val := getDetachNodeAnnotationValue(ing)

	v, ok := GetAnnotation(node.GetObjectMeta(), key)

	if val != nil && *val != v {
		return false, nil
	}

	if !ok {
		return false, nil
	}

	return true, nil
}

func (r *IngressDaemonSetReconciler) newDeployment(ing ingressdaemonsetsv1alpha1.IngressDaemonSet, nodeName string) appsv1.Deployment {
	deploymentName := fmt.Sprintf("%s-%s", ing.Name, nodeName)

	labels := map[string]string{
		"ingress-daemonset-node": deploymentName,
	}

	imagePullPolicy := ing.Spec.HealthChecker.ImagePullPolicy
	if imagePullPolicy == "" {
		imagePullPolicy = corev1.PullAlways
	}

	serviceAccountName := ing.Spec.HealthChecker.ServiceAccountName
	if serviceAccountName == "" {
		serviceAccountName = "default"
	}

	template := ing.Spec.Template.DeepCopy()
	template.GenerateName = deploymentName
	template.Labels = labels
	template.Spec.NodeName = nodeName

	deploy := appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{},
		ObjectMeta: metav1.ObjectMeta{
			Name:      deploymentName,
			Namespace: ing.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: ing.APIVersion,
					Kind:       ing.Kind,
					Name:       ing.Name,
					UID:        ing.UID,
				},
			},
			Annotations: map[string]string{},
		},
		Spec: appsv1.DeploymentSpec{
			Selector: metav1.SetAsLabelSelector(labels),
			Template: *template,
			Strategy: appsv1.DeploymentStrategy{
				Type: appsv1.RollingUpdateDeploymentStrategyType,
				RollingUpdate: &appsv1.RollingUpdateDeployment{
					MaxUnavailable: ing.Spec.UpdateStrategy.RollingUpdate.MaxUnavailablePodsPerNode,
					MaxSurge:       ing.Spec.UpdateStrategy.RollingUpdate.MaxSurgedPodsPerNode,
				},
			},
		},
	}

	deploy.Annotations[ObjectHashAnnotationKey] = ComputeHash(deploy.Spec)

	if err := ctrl.SetControllerReference(&ing, &deploy, r.Scheme); err != nil {
		panic(err)
	}

	return deploy
}

func (r *IngressDaemonSetReconciler) newDaemonSet(ing ingressdaemonsetsv1alpha1.IngressDaemonSet, port int) appsv1.DaemonSet {
	name := fmt.Sprintf("%s-%d", ing.Name, port)

	labels := map[string]string{
		"ingress-daemonset-hc": name,
	}

	maxUnavail := intstr.FromInt(1)

	imagePullPolicy := ing.Spec.HealthChecker.ImagePullPolicy
	if imagePullPolicy == "" {
		imagePullPolicy = corev1.PullAlways
	}

	serviceAccountName := ing.Spec.HealthChecker.ServiceAccountName
	if serviceAccountName == "" {
		serviceAccountName = "default"
	}

	image := ing.Spec.HealthChecker.Image
	if image == "" {
		image = "mumoshu/ingress-daemonset-node-healthcheck-server:latest"
	}

	ds := appsv1.DaemonSet{
		TypeMeta: metav1.TypeMeta{},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ing.Namespace,
			//OwnerReferences: []metav1.OwnerReference{
			//	{
			//		APIVersion: ing.APIVersion,
			//		Kind:       ing.Kind,
			//		Name:       ing.Name,
			//		UID:        ing.UID,
			//	},
			//},
			Annotations: map[string]string{
				HealthCheckNodePortAnnotation: fmt.Sprintf("%d", port),
			},
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: metav1.SetAsLabelSelector(labels),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: name,
					Labels:       labels,
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:            "node-healthcheck-server",
							Image:           image,
							ImagePullPolicy: imagePullPolicy,
							Command: []string{
								"/manager",
							},
							Args: []string{
								"--healthcheck-addr", fmt.Sprintf(":%d", port),
								"--namespace", ing.Namespace,
								"--deployment", ing.Name + "-$(NODE_NAME)",
								"--ingress-daemonset", ing.Name,
							},
							Env: []corev1.EnvVar{
								{
									Name: "NODE_NAME",
									ValueFrom: &corev1.EnvVarSource{
										FieldRef: &corev1.ObjectFieldSelector{
											FieldPath: "spec.nodeName",
										},
									},
								},
							},
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    *resource.NewMilliQuantity(100, resource.DecimalSI),
									corev1.ResourceMemory: *resource.NewMilliQuantity(100, resource.BinarySI),
								},
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    *resource.NewMilliQuantity(100, resource.DecimalSI),
									corev1.ResourceMemory: *resource.NewMilliQuantity(100, resource.BinarySI),
								},
							},
						},
					},
					RestartPolicy:      corev1.RestartPolicyAlways,
					ServiceAccountName: serviceAccountName,
					HostNetwork:        true,
				},
			},
			UpdateStrategy: appsv1.DaemonSetUpdateStrategy{
				Type: appsv1.RollingUpdateDaemonSetStrategyType,
				RollingUpdate: &appsv1.RollingUpdateDaemonSet{
					MaxUnavailable: &maxUnavail,
				},
			},
		},
	}

	ds.Annotations[ObjectHashAnnotationKey] = ComputeHash(ds.Spec)

	if err := ctrl.SetControllerReference(&ing, &ds, r.Scheme); err != nil {
		panic(err)
	}

	return ds
}

func (r *IngressDaemonSetReconciler) reconcileDaemonSets(
	ctx context.Context,
	log logr.Logger,
	ownedDaemonSetsList appsv1.DaemonSetList,
	ing ingressdaemonsetsv1alpha1.IngressDaemonSet,
) (*ctrl.Result, error) {

	desiredHealthCheckNodePorts := map[int]bool{}

	for _, p := range ing.Spec.HealthCheckNodePorts {
		desiredHealthCheckNodePorts[p] = true
	}

	for _, d := range ownedDaemonSetsList.Items {
		port, err := GetHealthCheckNodePort(d)
		if err != nil {
			return &ctrl.Result{}, err
		}

		if !desiredHealthCheckNodePorts[port] {
			if err := r.Delete(ctx, &d); err != nil {
				log.Error(err, "Failed to delete daemonest")

				return &ctrl.Result{RequeueAfter: defaultRequeueAfterOnError}, err
			}

			log.Info("Deleted daemonset", "daemonset", types.NamespacedName{Namespace: d.Namespace, Name: d.Name})

			continue
		}

		ds := r.newDaemonSet(ing, port)

		oldHash := d.Annotations[ObjectHashAnnotationKey]
		newHash := ds.Annotations[ObjectHashAnnotationKey]

		if oldHash != newHash {
			newDS := d.DeepCopy()
			newDS.Spec = ds.Spec

			if err := r.Update(ctx, newDS); err != nil {
				log.Error(err, "Updating daemonest")

				return &ctrl.Result{RequeueAfter: defaultRequeueAfterOnError}, err
			}
		}

		delete(desiredHealthCheckNodePorts, port)
	}

	for p := range desiredHealthCheckNodePorts {
		ds := r.newDaemonSet(ing, p)

		if err := r.Create(ctx, &ds); err != nil {
			log.Error(err, "Creating daemonest")

			return &ctrl.Result{RequeueAfter: defaultRequeueAfterOnError}, err
		}
	}

	return nil, nil
}

func (r *IngressDaemonSetReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(&appsv1.Deployment{}, DeploymentOwnerField, func(rawObj runtime.Object) []string {
		deploy := rawObj.(*appsv1.Deployment)

		owner := metav1.GetControllerOf(deploy)
		if owner == nil {
			return nil
		}

		if owner.Kind == "IngressDaemonSet" {
			return []string{owner.Name}
		}

		return []string{}
	}); err != nil {
		return err
	}

	if err := mgr.GetFieldIndexer().IndexField(&appsv1.DaemonSet{}, DaemonSetOwnerField, func(rawObj runtime.Object) []string {
		ds := rawObj.(*appsv1.DaemonSet)

		owner := metav1.GetControllerOf(ds)
		if owner == nil {
			return nil
		}

		if owner.Kind == "IngressDaemonSet" {
			return []string{owner.Name}
		}

		return []string{}
	}); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&ingressdaemonsetsv1alpha1.IngressDaemonSet{}).
		Owns(&appsv1.Deployment{}).
		Owns(&appsv1.DaemonSet{}).
		Complete(r)
}

func addFinalizer(finalizers []string) ([]string, bool) {
	exists := false
	for _, name := range finalizers {
		if name == finalizerName {
			exists = true
		}
	}

	if exists {
		return finalizers, false
	}

	return append(finalizers, finalizerName), true
}

func removeFinalizer(finalizers []string) ([]string, bool) {
	removed := false
	result := []string{}

	for _, name := range finalizers {
		if name == finalizerName {
			removed = true
			continue
		}
		result = append(result, name)
	}

	return result, removed
}

func SetAnnotation(meta metav1.Object, key string, value string) (map[string]string, bool) {
	m := meta.GetAnnotations()

	if m[key] != value {
		m[key] = value

		return m, true
	}

	return m, false
}

func GetAnnotation(meta metav1.Object, key string) (string, bool) {
	for k, v := range meta.GetAnnotations() {
		if k == key {
			return v, true
		}
	}

	return "", false
}

func GetHealthCheckNodePort(ds appsv1.DaemonSet) (int, error) {
	s, ok := GetAnnotation(ds.GetObjectMeta(), HealthCheckNodePortAnnotation)

	if ok && s != "" {
		i, err := strconv.Atoi(s)
		if err != nil {
			return -1, fmt.Errorf("converting healthCheckNodePort annotation value %s to int: %w", s, err)
		}

		return i, nil
	}

	return 0, fmt.Errorf("getting healthCheckNodePort annotation value: not found")
}

// ComputeHash returns a hash value calculated from pod template and
// a collisionCount to avoid hash collision. The hash will be safe encoded to
// avoid bad words.
//
// Proudly modified and adopted from k8s.io/kubernetes/pkg/util/hash.DeepHashObject and
// k8s.io/kubernetes/pkg/controller.ComputeHash.
func ComputeHash(obj interface{}) string {
	hasher := fnv.New32a()

	hasher.Reset()

	printer := spew.ConfigState{
		Indent:         " ",
		SortKeys:       true,
		DisableMethods: true,
		SpewKeys:       true,
	}
	printer.Fprintf(hasher, "%#v", obj)

	return rand.SafeEncodeString(fmt.Sprint(hasher.Sum32()))
}
