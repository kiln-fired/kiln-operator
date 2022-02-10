package controllers

import (
	bitcoinv1alpha1 "github.com/kiln-fired/kiln-operator/api/v1alpha1"
	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"testing"
)

func TestReconcileBitcoinNode_CreateStatefulset(t *testing.T) {

	b := &bitcoinv1alpha1.BitcoinNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-name",
			Namespace: "test-namespace",
		},
		Spec: bitcoinv1alpha1.BitcoinNodeSpec{
			RPCServer: bitcoinv1alpha1.RPCServer{
				CertSecret: "some-secret",
				User:       "some-user",
				Password:   "some-password",
			},
		},
	}

	r := makeTestReconciler(t, b)

	assert.NotNil(t, r.statefulsetForBitcoinNode(b))
}

func makeTestReconciler(t *testing.T, objs ...runtime.Object) *BitcoinNodeReconciler {
	s := scheme.Scheme
	assert.NoError(t, bitcoinv1alpha1.AddToScheme(s))

	cl := fake.NewClientBuilder().WithScheme(s).WithRuntimeObjects(objs...).Build()
	return &BitcoinNodeReconciler{
		Client: cl,
		Scheme: s,
	}
}
