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
	"path/filepath"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/nokia/k8s-ipam/pkg/meta"
	"github.com/nokia/k8s-ipam/pkg/resource"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/tools/clientcmd"

	porchv1alpha1 "github.com/GoogleContainerTools/kpt/porch/api/porch/v1alpha1"
	autov1alpha1 "github.com/henderiw-nephio/pkg-deployer/apis/automation/v1alpha1"
	ctrlconfig "github.com/henderiw-nephio/pkg-deployer/controllers/config"
	"github.com/henderiw-nephio/pkg-deployer/pkg/applicator"
	capiv1beta1 "sigs.k8s.io/cluster-api/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
	"sigs.k8s.io/kustomize/kyaml/kio"
	"sigs.k8s.io/kustomize/kyaml/kio/filters"
	"sigs.k8s.io/kustomize/kyaml/kio/kioutil"
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

	clusterHandler := &EnqueueRequestForAllClusters{
		client: mgr.GetClient(),
		ctx:    context.Background(),
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&autov1alpha1.PackageDeployment{}).
		Watches(&source.Kind{Type: &capiv1beta1.Cluster{}}, clusterHandler).
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

	pd := &autov1alpha1.PackageDeployment{}
	if err := r.Get(ctx, req.NamespacedName, pd); err != nil {
		// if the resource no longer exists the reconcile loop is done
		if resource.IgnoreNotFound(err) != nil {
			r.l.Error(err, "cannot get resource")
			return ctrl.Result{}, errors.Wrap(resource.IgnoreNotFound(err), "cannot get resource")
		}
		return reconcile.Result{}, nil
	}

	resources, err := r.getResources(ctx, pd)
	if err != nil {
		r.l.Error(err, "cannot get resources")
		return reconcile.Result{RequeueAfter: 5 * time.Second}, err
	}

	r.l.Info("resources", "nbr", len(resources))

	if len(resources) == 0 {
		return reconcile.Result{}, nil
	}

	clusters := &capiv1beta1.ClusterList{}
	if err := r.List(ctx, clusters); err != nil {
		r.l.Error(err, "cannot get clusters")
		return reconcile.Result{RequeueAfter: 5 * time.Second}, err
	}

	clusterNotReady := false
	for _, cluster := range clusters.Items {
		r.l.WithValues("cluster", cluster.GetName())
		r.l.Info("cluster status", "deleted", meta.WasDeleted(&cluster), "ready", getReadyStatus(cluster.GetConditions()))
		if meta.WasDeleted(&cluster) {
			continue
		}
		if !isReady(cluster.GetConditions()) {
			clusterNotReady = true
			continue
		}
		clusterClient, err := r.getClusterClient(ctx, &cluster)
		if err != nil {
			r.l.Error(err, "cannot get cluster client")
			return reconcile.Result{RequeueAfter: 5 * time.Second}, err
		}

		for _, resource := range resources {
			r.l.Info("install manifest", "resources",
				fmt.Sprintf("%s.%s.%s", resource.GetAPIVersion(), resource.GetKind(), resource.GetName()))
			if err := clusterClient.Apply(ctx, &resource); err != nil {
				r.l.Error(err, "cannot apply resource to cluster", "name", resource.GetName())
			}
		}
	}

	r.l.Info("done", "clusterNotReady", clusterNotReady)
	return ctrl.Result{}, nil
}

func (r *reconciler) getResources(ctx context.Context, pd *autov1alpha1.PackageDeployment) ([]unstructured.Unstructured, error) {
	prList := &porchv1alpha1.PackageRevisionList{}
	if err := r.porchClient.List(ctx, prList); err != nil {
		return nil, err
	}

	prKeys := []types.NamespacedName{}
	for _, pr := range prList.Items {
		for _, pkg := range pd.Spec.Packages {
			// TODO namespace

			/*
				r.l.Info("get resources",
					"PackageName", fmt.Sprintf("%s=%s", pkg.PackageName, pr.Spec.PackageName),
					"RepositoryName", fmt.Sprintf("%s=%s", pkg.RepositoryName, pr.Spec.RepositoryName),
					"Revision", fmt.Sprintf("%s=%s", pkg.Revision, pr.Spec.Revision),
				)
			*/
			if pkg.PackageName == pr.Spec.PackageName &&
				pkg.RepositoryName == pr.Spec.RepositoryName &&
				pkg.Revision == pr.Spec.Revision {
				prKeys = append(prKeys, types.NamespacedName{Name: pr.GetName(), Namespace: pr.GetNamespace()})
			}
		}
	}
	//r.l.Info("key matches", "keys", prKeys)

	resources := []unstructured.Unstructured{}
	for _, prKey := range prKeys {
		prr := &porchv1alpha1.PackageRevisionResources{}
		if err := r.porchClient.Get(ctx, prKey, prr); err != nil {
			r.l.Error(err, "cannot get package resvision resourcelist", "key", prKey)
			return nil, err
		}

		res, err := r.getResourcesPRR(prr.Spec.Resources)
		if err != nil {
			r.l.Error(err, "cannot get resources", "key", prKey)
			return nil, err
		}
		resources = append(resources, res...)
	}
	return resources, nil
}

func (r *reconciler) getClusterClient(ctx context.Context, cr *capiv1beta1.Cluster) (*applicator.APIPatchingApplicator, error) {
	secret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{
		Name:      fmt.Sprintf("%s-kubeconfig", cr.GetName()),
		Namespace: cr.GetNamespace(),
	}, secret); err != nil {
		r.l.Error(err, "cannot get secret")
		return nil, err
	}

	//r.l.Info("cluster", "config", string(secret.Data["value"]))

	config, err := clientcmd.RESTConfigFromKubeConfig(secret.Data["value"])
	if err != nil {
		r.l.Error(err, "cannot get rest Config from kubeconfig")
		return nil, err
	}
	clClient, err := client.New(config, client.Options{})
	if err != nil {
		r.l.Error(err, "cannot get client from rest config")
		return nil, err
	}
	return applicator.NewAPIPatchingApplicator(clClient), nil
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

func includeFile(path string, match []string) bool {
	for _, m := range match {
		file := filepath.Base(path)
		if matched, err := filepath.Match(m, file); err == nil && matched {
			return true
		}
	}
	return false
}

func (r *reconciler) getResourcesPRR(resources map[string]string) ([]unstructured.Unstructured, error) {
	inputs := []kio.Reader{}
	for path, data := range resources {
		if includeFile(path, []string{"*.yaml", "*.yml", "Kptfile"}) {
			inputs = append(inputs, &kio.ByteReader{
				Reader: strings.NewReader(data),
				SetAnnotations: map[string]string{
					kioutil.PathAnnotation: path,
				},
				DisableUnwrapping: true,
			})
		}
	}
	var pb kio.PackageBuffer
	err := kio.Pipeline{
		Inputs:  inputs,
		Filters: []kio.Filter{},
		Outputs: []kio.Writer{&pb},
	}.Execute()
	if err != nil {
		return nil, err
	}

	ul := []unstructured.Unstructured{}
	for _, n := range pb.Nodes {
		if v, ok := n.GetAnnotations()[filters.LocalConfigAnnotation]; ok && v == "true" {
			continue
		}
		u := unstructured.Unstructured{}
		if err := yaml.Unmarshal([]byte(n.MustString()), &u); err != nil {
			r.l.Error(err, "cannot unmarshal data", "data", n.MustString())
			// we dont fail
			continue
		}
		ul = append(ul, u)
	}
	return ul, nil
}
