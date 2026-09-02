package restore

import (
	"context"
	"fmt"
	"os"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/retry"

	"github.com/azuki774/kinakomate/internal/config"
)

// kubernetesClient is the real Kubernetes implementation of the runner's
// Kubernetes interface. It talks to the cluster API (in-cluster when running
// as the CronJob) and scales Deployments/StatefulSets by name.
//
// The runner never reads Secrets from the Kubernetes API; it only scales the
// configured workloads, which is in scope for the restore-test-runner.
type kubernetesClient struct {
	clientset kubernetes.Interface
	namespace string
}

// newKubernetesClient builds a client from the in-cluster configuration. It
// does not contact the API server; the connection is verified lazily by
// CheckConnection.
func newKubernetesClient() (*kubernetesClient, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to build in-cluster kube config: %w", err)
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to build kubernetes client: %w", err)
	}
	return &kubernetesClient{
		clientset: cs,
		namespace: namespace(),
	}, nil
}

// namespace resolves the target namespace. When running in-cluster the
// POD_NAMESPACE env is set by Kubernetes; otherwise fall back to "default".
func namespace() string {
	if ns := os.Getenv("POD_NAMESPACE"); ns != "" {
		return ns
	}
	return "default"
}

// CheckConnection verifies the runner can reach the Kubernetes API.
func (k *kubernetesClient) CheckConnection(ctx context.Context, _ *config.Config) error {
	if _, err := k.clientset.Discovery().ServerVersion(); err != nil {
		return fmt.Errorf("kubernetes api unreachable: %w", err)
	}
	return nil
}

// GetReplicas returns the desired replica count of the named workload,
// trying Deployment then StatefulSet.
func (k *kubernetesClient) GetReplicas(ctx context.Context, _ *config.Config, workload string) (int, error) {
	if dep, err := k.clientset.AppsV1().Deployments(k.namespace).Get(ctx, workload, metav1.GetOptions{}); err == nil {
		if dep.Spec.Replicas == nil {
			return 0, nil
		}
		return int(*dep.Spec.Replicas), nil
	} else if !apierrors.IsNotFound(err) {
		return 0, fmt.Errorf("failed to get workload %q: %w", workload, err)
	}

	if sts, err := k.clientset.AppsV1().StatefulSets(k.namespace).Get(ctx, workload, metav1.GetOptions{}); err == nil {
		if sts.Spec.Replicas == nil {
			return 0, nil
		}
		return int(*sts.Spec.Replicas), nil
	} else if !apierrors.IsNotFound(err) {
		return 0, fmt.Errorf("failed to get workload %q: %w", workload, err)
	}

	return 0, fmt.Errorf("workload %q not found as Deployment or StatefulSet in namespace %q", workload, k.namespace)
}

// Scale sets the replica count of the named workload, trying Deployment then
// StatefulSet. Controllers write workload objects concurrently (e.g. status
// updates right after a scale), so a read-modify-write Update can hit a
// resourceVersion conflict; the attempt is re-GET and retried instead of
// failing the whole restore-test step.
func (k *kubernetesClient) Scale(ctx context.Context, _ *config.Config, workload string, replicas int) error {
	replicas32 := int32(replicas)

	var kind string
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var scaleErr error
		kind, scaleErr = k.scaleOnce(ctx, workload, replicas32)
		return scaleErr
	})
	if err == nil {
		return nil
	}
	if kind == "" {
		return err
	}
	return fmt.Errorf("failed to scale %s %q to %d: %w", kind, workload, replicas, err)
}

// scaleOnce performs a single read-modify-write scale attempt against the
// Deployment, falling back to the StatefulSet. It returns which kind it acted
// on ("deployment" / "statefulset") or "" when neither matched, plus the
// update error unwrapped: retry.RetryOnConflict must see the raw conflict
// error, so message wrapping happens in Scale after the retry loop concludes.
func (k *kubernetesClient) scaleOnce(ctx context.Context, workload string, replicas int32) (string, error) {
	if dep, err := k.clientset.AppsV1().Deployments(k.namespace).Get(ctx, workload, metav1.GetOptions{}); err == nil {
		dep.Spec.Replicas = &replicas
		if _, err := k.clientset.AppsV1().Deployments(k.namespace).Update(ctx, dep, metav1.UpdateOptions{}); err != nil {
			return "deployment", err
		}
		return "deployment", nil
	} else if !apierrors.IsNotFound(err) {
		return "", fmt.Errorf("failed to get deployment %q: %w", workload, err)
	}

	if sts, err := k.clientset.AppsV1().StatefulSets(k.namespace).Get(ctx, workload, metav1.GetOptions{}); err == nil {
		sts.Spec.Replicas = &replicas
		if _, err := k.clientset.AppsV1().StatefulSets(k.namespace).Update(ctx, sts, metav1.UpdateOptions{}); err != nil {
			return "statefulset", err
		}
		return "statefulset", nil
	} else if !apierrors.IsNotFound(err) {
		return "", fmt.Errorf("failed to get statefulset %q: %w", workload, err)
	}

	return "", fmt.Errorf("workload %q not found as Deployment or StatefulSet in namespace %q", workload, k.namespace)
}

// WaitForReplicas polls the actual (status) replica count of the named
// workload until it equals want, or the timeout elapses. It fails on timeout so
// the runner can stop and roll back.
func (k *kubernetesClient) WaitForReplicas(ctx context.Context, _ *config.Config, workload string, want int, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(scalePollInterval)
	defer ticker.Stop()

	for {
		got, err := k.statusReplicas(ctx, workload)
		if err == nil && got == want {
			return nil
		}
		if ctx.Err() != nil {
			return fmt.Errorf("timed out waiting for %q replicas to become %d (last observed %d): %w", workload, want, got, ctx.Err())
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for %q replicas to become %d (last observed %d)", workload, want, got)
		case <-ticker.C:
		}
	}
}

// statusReplicas returns the current actual replica count (status) of the
// named workload, trying Deployment then StatefulSet.
func (k *kubernetesClient) statusReplicas(ctx context.Context, workload string) (int, error) {
	if dep, err := k.clientset.AppsV1().Deployments(k.namespace).Get(ctx, workload, metav1.GetOptions{}); err == nil {
		return int(dep.Status.Replicas), nil
	} else if !apierrors.IsNotFound(err) {
		return 0, err
	}
	if sts, err := k.clientset.AppsV1().StatefulSets(k.namespace).Get(ctx, workload, metav1.GetOptions{}); err == nil {
		return int(sts.Status.Replicas), nil
	} else if !apierrors.IsNotFound(err) {
		return 0, err
	}
	return 0, fmt.Errorf("workload %q not found as Deployment or StatefulSet in namespace %q", workload, k.namespace)
}
