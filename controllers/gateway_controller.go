/*
Copyright 2023.

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
	"k8s.io/apimachinery/pkg/types"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	podgatewayv1alpha1 "github.com/maxgio92/my-operator-kubebuilder/api/v1alpha1"
)

const (
	gatewayContainerImage = "nginx:latest"
	gatewayFinalizer      = "podgateway.maxgio.me/finalizer"
)

// GatewayReconciler reconciles a Gateway object
type GatewayReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=podgateway.maxgio.me,resources=gateways,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=podgateway.maxgio.me,resources=gateways/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=podgateway.maxgio.me,resources=gateways/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Gateway object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.14.1/pkg/reconcile
func (r *GatewayReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Retrieve the Gateway object named in the reques.
	var gtw podgatewayv1alpha1.Gateway
	err := r.Get(ctx, req.NamespacedName, &gtw)
	if err != nil {
		logger.Error(err, "could not retrieve gateway object")

		// Ignore not found errors - could be caused because the object is deleting.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	var pod v1.Pod

	// If no gateway pod exists, create one.
	if err := r.Get(ctx, req.NamespacedName, &pod); err != nil {
		if client.IgnoreNotFound(err) != nil {
			return ctrl.Result{}, err
		}

		podSpec := createPodSpec(gtw)

		pod.Spec = podSpec
		pod.Name = gtw.Name
		pod.Namespace = gtw.Namespace
		if err := r.Create(ctx, &pod); err != nil {
			logger.Error(err, "could not create gateway pod")
			return ctrl.Result{RequeueAfter: time.Second * 5}, nil
		}
	}

	// The gateway pod exists, update the Gateway object status.
	switch pod.Status.Phase {
	case v1.PodPending:
		gtw.Status.Phase = podgatewayv1alpha1.GatewayPending
	case v1.PodRunning:
		gtw.Status.Phase = podgatewayv1alpha1.GatewayUp
	default:
		gtw.Status.Phase = podgatewayv1alpha1.GatewayFailed
	}
	r.Status().Update(ctx, &gtw)

	// Register the finalizer for the Gateway object.
	if result, err := r.registerFinalizer(ctx, &gtw); err != nil {
		logger.Error(err, "could not register finalizer")
		return result, err
	}

	// If the Gateway object is in deleting state, cleanup the dependent resources.
	if objectDeleting(&gtw) {
		err := r.deleteExternalResources(ctx, &gtw)
		return ctrl.Result{}, err
	}

	logger.Info("Status ", "name", pod.Name, "pod phase ", pod.Status.Phase, "Gateway phase ", gtw.Status.Phase)

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *GatewayReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&podgatewayv1alpha1.Gateway{}).
		Complete(r)
}

func (r *GatewayReconciler) registerFinalizer(ctx context.Context, gtw *podgatewayv1alpha1.Gateway) (ctrl.Result, error) {
	var err error

	// Examine DeletionTimestamp to determine if object is under deletion.
	if !objectDeleting(gtw) {

		// The object is not being deleted, so if it does not have our finalizer,
		// then let's add the finalizer and update the object.
		// This is equivalent to register our finalizer.
		if !controllerutil.ContainsFinalizer(gtw, gatewayFinalizer) {
			controllerutil.AddFinalizer(gtw, gatewayFinalizer)
			err = r.Update(ctx, gtw)
		}
	}

	return ctrl.Result{}, err
}

func (r *GatewayReconciler) deleteExternalResources(ctx context.Context, gateway *podgatewayv1alpha1.Gateway) error {
	var pod v1.Pod

	logger := log.FromContext(ctx)

	if controllerutil.ContainsFinalizer(gateway, gatewayFinalizer) {

		// Our finalier is present, so let's handle any external dependency.
		if err := r.Get(ctx, GetPodNamespacedName(*gateway), &pod); err == nil {
			var policy metav1.DeletionPropagation
			policy = metav1.DeletePropagationForeground

			if err := r.Delete(ctx, &pod, &client.DeleteOptions{PropagationPolicy: &policy}); err != nil {
				logger.Error(err, "could not delete gateway pod")
				return err
			}
		}
	}

	// Remove our finalizer from the list and update it.
	controllerutil.RemoveFinalizer(gateway, gatewayFinalizer)

	return r.Update(ctx, gateway)
}

func objectDeleting(gateway *podgatewayv1alpha1.Gateway) bool {
	return !gateway.ObjectMeta.DeletionTimestamp.IsZero()
}

func createPodSpec(gateway podgatewayv1alpha1.Gateway) v1.PodSpec {
	container := v1.Container{
		Name:  getPodName(gateway),
		Image: gatewayContainerImage,
		Ports: []v1.ContainerPort{{ContainerPort: 80}},
	}

	result := v1.PodSpec{
		Containers: []v1.Container{container},
	}

	return result
}

func GetPodNamespacedName(gateway podgatewayv1alpha1.Gateway) types.NamespacedName {
	return types.NamespacedName{
		Name:      getPodName(gateway),
		Namespace: gateway.Namespace,
	}
}

func getPodName(gateway podgatewayv1alpha1.Gateway) string {
	return gateway.Name
}
