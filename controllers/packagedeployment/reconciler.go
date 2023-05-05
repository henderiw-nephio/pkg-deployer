/*
Copyright 2023 The Nephio Authors.

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

package packagedeployment

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	"github.com/nokia/k8s-ipam/pkg/meta"
	"github.com/nokia/k8s-ipam/pkg/resource"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	//autov1alpha1 "github.com/henderiw-nephio/pkg-deployer/apis/automation/v1alpha1"
	ctrlconfig "github.com/henderiw-nephio/pkg-deployer/controllers/config"
	capiv1beta1 "sigs.k8s.io/cluster-api/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

//+kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=porch.kpt.dev,resources=packagerevisions,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=porch.kpt.dev,resources=packagerevisions/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=*,resources=networkinstances,verbs=get;list;watch

// SetupWithManager sets up the controller with the Manager.
func Setup(mgr ctrl.Manager, options *ctrlconfig.ControllerConfig) error {
	r := &reconciler{
		Client:      mgr.GetClient(),
		porchClient: options.PorchClient,
	}

	//clusterHandler := &EnqueueRequestForAllClusters{
	//	client: mgr.GetClient(),
	//	ctx:    context.Background(),
	//}

	return ctrl.NewControllerManagedBy(mgr).
		For(&capiv1beta1.Cluster{}).
		//Watches(&source.Kind{Type: &capiv1beta1.Cluster{}}, clusterHandler).
		Complete(r)
}

type reconciler struct {
	client.Client
	porchClient client.Client

	l logr.Logger
}

func (r *reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	r.l = log.FromContext(ctx)
	r.l.Info("reconcile", "req", req)

	cr := &capiv1beta1.Cluster{}
	if err := r.Get(ctx, req.NamespacedName, cr); err != nil {
		// There's no need to requeue if we no longer exist. Otherwise we'll be
		// requeued implicitly because we return an error.
		if resource.IgnoreNotFound(err) != nil {
			r.l.Error(err, "cannot get resource")
			return ctrl.Result{}, errors.Wrap(resource.IgnoreNotFound(err), "cannot get resource")
		}
		return reconcile.Result{}, nil
	}

	r.l.Info("reconcile status", "ready status", getReadyStatus(cr.GetConditions()))

	if meta.WasDeleted(cr) {
		r.l.Info("resource deleted")
		return reconcile.Result{}, nil
	}

	if isReady(cr.GetConditions()) {
		r.l.Info("resource ready")

		secret := &corev1.Secret{}
		if err := r.Get(ctx, types.NamespacedName{
			Name:      fmt.Sprintf("%s-kubeconfig", cr.GetName()),
			Namespace: cr.GetNamespace(),
		}, secret); err != nil {
			r.l.Error(err, "cannot get secret")
			return reconcile.Result{RequeueAfter: 5 * time.Second}, err
		}

		r.l.Info("cluster", "config", string(secret.Data["value"]))

		config, err := clientcmd.RESTConfigFromKubeConfig(secret.Data["value"])
		if err != nil {
			r.l.Error(err, "cannot get rest Config from kubeconfig")
			return reconcile.Result{RequeueAfter: 5 * time.Second}, err
		}
		r.l.Info("cluster", "rest config", config)
		clientset, err := kubernetes.NewForConfig(config)
		if err != nil {
			r.l.Error(err, "cannot get rest Config from kubeconfig")
			return reconcile.Result{RequeueAfter: 5 * time.Second}, err
		}

		pods, err := clientset.CoreV1().Pods(cr.GetNamespace()).List(ctx, v1.ListOptions{})
		if err != nil {
			r.l.Error(err, "cannot get pods")
			return reconcile.Result{RequeueAfter: 5 * time.Second}, err
		}
		r.l.Info("cluster", "pods", pods)
	}

	return ctrl.Result{}, nil
}

func getReadyStatus(cs capiv1beta1.Conditions) capiv1beta1.Condition {
	for _, c := range cs {
		if c.Type == capiv1beta1.ReadyCondition {
			return c
		}
	}
	return capiv1beta1.Condition{}
}

func isReady(cs capiv1beta1.Conditions) bool {
	for _, c := range cs {
		if c.Type == capiv1beta1.ReadyCondition {
			if c.Status == corev1.ConditionTrue {
				return true
			}
		}
	}
	return false
}
