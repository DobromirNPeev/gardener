// SPDX-FileCopyrightText: Contributors to the Gardener project
//
// SPDX-License-Identifier: Apache-2.0

package rootcacertlabeler

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/gardener/gardener/pkg/controllerutils"
)

// ControllerName is the name of the controller.
const ControllerName = "root-ca-cert-labeler"

// AddToManager adds Reconciler to the given manager.
func (r *Reconciler) AddToManager(mgr manager.Manager, targetCluster cluster.Cluster) error {
	if r.TargetClient == nil {
		r.TargetClient = targetCluster.GetClient()
	}

	return builder.
		ControllerManagedBy(mgr).
		Named(ControllerName).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: int(ptr.Deref(r.Config.ConcurrentSyncs, 0)),
			ReconciliationTimeout:   controllerutils.DefaultReconciliationTimeout,
		}).
		WatchesRawSource(
			source.Kind[client.Object](targetCluster.GetCache(),
				&corev1.ConfigMap{},
				&handler.EnqueueRequestForObject{},
				r.ConfigMapPredicate()),
		).
		Complete(r)
}

// ConfigMapPredicate returns a predicate that filters for the kube-root-ca.crt ConfigMap.
func (r *Reconciler) ConfigMapPredicate() predicate.Predicate {
	return predicate.NewPredicateFuncs(func(obj client.Object) bool {
		return obj.GetName() == ConfigMapName
	})
}
