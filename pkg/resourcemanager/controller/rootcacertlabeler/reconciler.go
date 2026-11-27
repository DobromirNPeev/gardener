// SPDX-FileCopyrightText: Contributors to the Gardener project
//
// SPDX-License-Identifier: Apache-2.0

package rootcacertlabeler

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	resourcemanagerconfigv1alpha1 "github.com/gardener/gardener/pkg/apis/config/resourcemanager/v1alpha1"
	v1beta1constants "github.com/gardener/gardener/pkg/apis/core/v1beta1/constants"
)

// ConfigMapName is the name of the kube-root-ca.crt ConfigMap that exists in every namespace and holds the cluster CA
// bundle.
const ConfigMapName = "kube-root-ca.crt"

// Reconciler stamps a stable label on kube-root-ca.crt ConfigMaps so that webhook remediation can precisely exclude
// them from customer webhooks, keeping them writable during credential rotation.
type Reconciler struct {
	TargetClient client.Client
	Config       resourcemanagerconfigv1alpha1.RootCACertLabelerControllerConfig
}

// Reconcile adds the label to the kube-root-ca.crt ConfigMap if it is not present yet.
func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	log := logf.FromContext(ctx)

	configMap := &corev1.ConfigMap{}
	if err := r.TargetClient.Get(ctx, req.NamespacedName, configMap); err != nil {
		if apierrors.IsNotFound(err) {
			log.V(1).Info("Object is gone, stop reconciling")
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, fmt.Errorf("error retrieving object from store: %w", err)
	}

	// Predicates only filter watch events but don't filter when an object (or rather a reconcile.Request) is already in
	// the queue. Hence, we should always check whether there is work left to do in the controller in addition to
	// predicates.
	if configMap.Name != ConfigMapName {
		return reconcile.Result{}, nil
	}

	if configMap.Labels[v1beta1constants.LabelKeyKubeRootCACrt] == "true" {
		return reconcile.Result{}, nil
	}

	log.Info("Adding label to kube-root-ca.crt ConfigMap", "label", v1beta1constants.LabelKeyKubeRootCACrt)

	patch := client.MergeFrom(configMap.DeepCopy())
	if configMap.Labels == nil {
		configMap.Labels = make(map[string]string, 1)
	}
	configMap.Labels[v1beta1constants.LabelKeyKubeRootCACrt] = "true"

	return reconcile.Result{}, r.TargetClient.Patch(ctx, configMap, patch)
}
