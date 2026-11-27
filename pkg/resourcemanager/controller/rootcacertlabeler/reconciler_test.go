// SPDX-FileCopyrightText: Contributors to the Gardener project
//
// SPDX-License-Identifier: Apache-2.0

package rootcacertlabeler_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1beta1constants "github.com/gardener/gardener/pkg/apis/core/v1beta1/constants"
	. "github.com/gardener/gardener/pkg/resourcemanager/controller/rootcacertlabeler"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(corev1.AddToScheme(scheme))
}

var _ = Describe("Reconciler", func() {
	var (
		ctx        = context.Background()
		fakeClient client.Client
		reconciler *Reconciler
	)

	BeforeEach(func() {
		fakeClient = fakeclient.NewClientBuilder().WithScheme(scheme).Build()
		reconciler = &Reconciler{TargetClient: fakeClient}
	})

	reconcile := func(configMap *corev1.ConfigMap) {
		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(configMap)})
		Expect(err).NotTo(HaveOccurred())
	}

	It("should add the label to a kube-root-ca.crt ConfigMap", func() {
		configMap := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: ConfigMapName, Namespace: "foo"}}
		Expect(fakeClient.Create(ctx, configMap)).To(Succeed())

		reconcile(configMap)

		Expect(fakeClient.Get(ctx, client.ObjectKeyFromObject(configMap), configMap)).To(Succeed())
		Expect(configMap.Labels).To(HaveKeyWithValue(v1beta1constants.LabelKeyKubeRootCACrt, "true"))
	})

	It("should preserve existing labels when adding the label", func() {
		configMap := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
			Name:      ConfigMapName,
			Namespace: "foo",
			Labels:    map[string]string{"existing": "label"},
		}}
		Expect(fakeClient.Create(ctx, configMap)).To(Succeed())

		reconcile(configMap)

		Expect(fakeClient.Get(ctx, client.ObjectKeyFromObject(configMap), configMap)).To(Succeed())
		Expect(configMap.Labels).To(HaveKeyWithValue(v1beta1constants.LabelKeyKubeRootCACrt, "true"))
		Expect(configMap.Labels).To(HaveKeyWithValue("existing", "label"))
	})

	It("should not add the label to an unrelated ConfigMap", func() {
		configMap := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "some-other-configmap", Namespace: "foo"}}
		Expect(fakeClient.Create(ctx, configMap)).To(Succeed())

		reconcile(configMap)

		Expect(fakeClient.Get(ctx, client.ObjectKeyFromObject(configMap), configMap)).To(Succeed())
		Expect(configMap.Labels).NotTo(HaveKey(v1beta1constants.LabelKeyKubeRootCACrt))
	})

	It("should be a no-op for an already labeled kube-root-ca.crt ConfigMap", func() {
		configMap := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
			Name:      ConfigMapName,
			Namespace: "foo",
			Labels:    map[string]string{v1beta1constants.LabelKeyKubeRootCACrt: "true"},
		}}
		Expect(fakeClient.Create(ctx, configMap)).To(Succeed())
		resourceVersionBefore := configMap.ResourceVersion

		reconcile(configMap)

		Expect(fakeClient.Get(ctx, client.ObjectKeyFromObject(configMap), configMap)).To(Succeed())
		Expect(configMap.Labels).To(HaveKeyWithValue(v1beta1constants.LabelKeyKubeRootCACrt, "true"))
		Expect(configMap.ResourceVersion).To(Equal(resourceVersionBefore))
	})

	It("should not fail if the ConfigMap is gone", func() {
		configMap := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: ConfigMapName, Namespace: "foo"}}

		reconcile(configMap)
	})
})
