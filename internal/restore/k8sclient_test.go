package restore

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/azuki774/kinakomate/internal/config"
)

func newFakeK8s(t *testing.T, webReplicas, webStatus, dbReplicas int32) *kubernetesClient {
	t.Helper()
	web := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"},
		Spec:       appsv1.DeploymentSpec{Replicas: &webReplicas},
		Status:     appsv1.DeploymentStatus{Replicas: webStatus},
	}
	db := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "default"},
		Spec:       appsv1.StatefulSetSpec{Replicas: &dbReplicas},
	}
	return &kubernetesClient{clientset: fake.NewSimpleClientset(web, db), namespace: "default"}
}

func TestKubernetesClient_GetReplicas(t *testing.T) {
	k := newFakeK8s(t, 3, 3, 1)

	if n, err := k.GetReplicas(context.Background(), &config.Config{}, "web"); err != nil || n != 3 {
		t.Fatalf("GetReplicas(web) = (%d, %v), want (3, nil)", n, err)
	}
	if n, err := k.GetReplicas(context.Background(), &config.Config{}, "db"); err != nil || n != 1 {
		t.Fatalf("GetReplicas(db) = (%d, %v), want (1, nil)", n, err)
	}
	if _, err := k.GetReplicas(context.Background(), &config.Config{}, "missing"); err == nil {
		t.Fatal("GetReplicas(missing) expected error")
	}
}

func TestKubernetesClient_Scale(t *testing.T) {
	k := newFakeK8s(t, 3, 3, 1)

	if err := k.Scale(context.Background(), &config.Config{}, "web", 0); err != nil {
		t.Fatalf("Scale(web,0) error: %v", err)
	}
	if n, err := k.GetReplicas(context.Background(), &config.Config{}, "web"); err != nil || n != 0 {
		t.Fatalf("after Scale(web,0) GetReplicas = (%d, %v), want (0, nil)", n, err)
	}

	// db is a StatefulSet; scaling it must update the StatefulSet, not fail.
	if err := k.Scale(context.Background(), &config.Config{}, "db", 2); err != nil {
		t.Fatalf("Scale(db,2) error: %v", err)
	}
	if n, err := k.GetReplicas(context.Background(), &config.Config{}, "db"); err != nil || n != 2 {
		t.Fatalf("after Scale(db,2) GetReplicas = (%d, %v), want (2, nil)", n, err)
	}
}

func TestKubernetesClient_WaitForReplicas(t *testing.T) {
	// web status already at 0, so WaitForReplicas(0) returns immediately.
	k := newFakeK8s(t, 0, 0, 1)
	if err := k.WaitForReplicas(context.Background(), &config.Config{}, "web", 0, 2*time.Second); err != nil {
		t.Fatalf("WaitForReplicas(web,0) unexpected error: %v", err)
	}

	// status is 2 but want 0; with a short timeout it must fail.
	k2 := newFakeK8s(t, 2, 2, 1)
	if err := k2.WaitForReplicas(context.Background(), &config.Config{}, "web", 0, 100*time.Millisecond); err == nil {
		t.Fatal("WaitForReplicas(web,0) expected timeout error")
	}
}

func TestKubernetesClient_CheckConnection(t *testing.T) {
	k := newFakeK8s(t, 1, 1, 1)
	if err := k.CheckConnection(context.Background(), &config.Config{}); err != nil {
		t.Fatalf("CheckConnection error: %v", err)
	}
}
