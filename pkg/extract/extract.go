package extract

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/client-go/util/homedir"
)

const (
	img     = "library/busybox:1.37.0"
	podName = "extractor"
)

// DumpOTelCollector mounts a temporary container with the PVC, given its namespace and name,
// and copies all data into the provided directory (creates it if necessary).
func DumpOTelCollector(
	ctx context.Context,
	namespace, pvcName, into string,
	opts ...Option,
) error {
	// Prepare functional options
	options := &options{
		logger: zap.NewNop(),
	}
	for _, opt := range opts {
		opt.apply(options)
	}

	// Prepare K8s client
	clientset, config, err := getClient()
	if err != nil {
		return err
	}

	// Create Pod and mount PVC
	options.logger.Info("creating Pod",
		zap.String("pod", podName),
		zap.String("namespace", namespace),
		zap.String("pvc", pvcName),
	)

	if _, err := clientset.CoreV1().Pods(namespace).Create(ctx, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      podName,
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{
				{
					Name: "copy",
					Image: func() string {
						if options.registry != "" && !strings.HasSuffix(options.registry, "/") {
							options.registry += "/"
						}
						return options.registry + img
					}(),
					Command: []string{"/bin/sh", "-c", "--"},
					Args:    []string{"sleep infinity"},
					VolumeMounts: []corev1.VolumeMount{
						{
							Name:      "data",
							MountPath: "/data",
						},
					},
					// The following matches the pod security policy "restricted".
					// It is not required for the extractor to work, but is a good
					// practice, plus we don't need large capabilities so it's OK.
					SecurityContext: &corev1.SecurityContext{
						AllowPrivilegeEscalation: ptr(false),
						Capabilities: &corev1.Capabilities{
							Drop: []corev1.Capability{"ALL"},
						},
						RunAsUser:    ptr(int64(1000)), // Don't need to be root !
						RunAsNonRoot: ptr(true),
						SeccompProfile: &corev1.SeccompProfile{
							Type: corev1.SeccompProfileTypeRuntimeDefault,
						},
					},
				},
			},
			Volumes: []corev1.Volume{
				{
					Name: "data",
					VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
							ClaimName: pvcName,
						},
					},
				},
			},
		},
	}, metav1.CreateOptions{}); err != nil {
		return err
	}

	// Wait for it to be Up & Running
	options.logger.Info("waiting for the pod to be ready",
		zap.String("pod", podName),
		zap.String("namespace", namespace),
	)
	if err := waitForPodReady(ctx, clientset, namespace, podName); err != nil {
		return err
	}

	// Copy files
	options.logger.Info("copying files",
		zap.String("directory", into),
	)
	if err := copyFromPod(ctx, config, clientset, namespace, podName, "copy", "/data", into); err != nil {
		return err
	}

	// Delete Pod
	options.logger.Info("deleting pod",
		zap.String("pod", podName),
		zap.String("namespace", namespace),
	)
	return clientset.CoreV1().Pods(namespace).Delete(ctx, podName, metav1.DeleteOptions{})
}

func getClient() (*kubernetes.Clientset, *rest.Config, error) {
	kubeconfig := filepath.Join(homedir.HomeDir(), ".kube", "config")
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, nil, err
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, nil, err
	}
	return clientset, config, nil
}

func waitForPodReady(ctx context.Context, clientset *kubernetes.Clientset, namespace, podName string) error {
	return wait.PollUntilContextTimeout(ctx, 2*time.Second, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
		pod, err := clientset.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		for _, cond := range pod.Status.Conditions {
			if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
				return true, nil
			}
		}
		return false, nil
	})
}

func copyFromPod(
	ctx context.Context,
	config *rest.Config,
	clientset *kubernetes.Clientset,
	namespace, podName, containerName, podPath, localDir string,
) error {
	req := clientset.CoreV1().RESTClient().
		Get().
		Namespace(namespace).
		Resource("pods").
		Name(podName).
		SubResource("exec").
		Param("container", containerName).
		Param("stdout", "true").
		Param("stderr", "true").
		Param("command", "tar").
		Param("command", "cf").
		Param("command", "-").
		Param("command", "-C").
		Param("command", podPath).
		Param("command", ".")

	exec, err := remotecommand.NewSPDYExecutor(config, "POST", req.URL())
	if err != nil {
		return err
	}

	var stdout, stderr bytes.Buffer
	err = exec.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdout: &stdout,
		Stderr: &stderr,
		Tty:    false,
	})
	if err != nil {
		return fmt.Errorf("stream error: %v\nstderr: %s", err, stderr.String())
	}

	// untar locally
	return untar(bytes.NewReader(stdout.Bytes()), localDir)
}

func untar(r io.Reader, dest string) error {
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		target, err := sanitizeArchivePath(dest, hdr.Name)
		if err != nil {
			// tainted path, could be a Path Traversal
			return err
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(path.Dir(target), 0755); err != nil {
				return err
			}
			f, err := os.Create(target)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			f.Close()
		}
	}
	return nil
}

// Based upon https://security.snyk.io/research/zip-slip-vulnerability#expandable-socPI9fFAJ-title
func sanitizeArchivePath(destination, filePath string) (destpath string, err error) {
	destpath = filepath.Join(destination, filePath)
	if !strings.HasPrefix(destpath, filepath.Clean(destination)+string(os.PathSeparator)) {
		return destpath, fmt.Errorf("filepath is tainted: %s", destination)
	}
	return
}

func ptr[T any](t T) *T {
	return &t
}
