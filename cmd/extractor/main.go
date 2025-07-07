package main

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sync"
	"time"

	"github.com/urfave/cli/v2"
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

var (
	version = "dev"
	commit  = ""
	date    = ""
	builtBy = ""

	logger     *zap.Logger
	loggerOnce sync.Once
)

func main() {
	app := &cli.App{
		Name:  "Monitoring Extractor",
		Usage: "Extract the Monitoring files from an OpenTelemetry Collector.",
		Flags: []cli.Flag{
			cli.VersionFlag,
			cli.HelpFlag,
			&cli.StringFlag{
				Name:     "namespace",
				EnvVars:  []string{"NAMESPACE"},
				Required: true,
				Usage:    "The namespace in which to deploy the extraction Pod.",
			},
			&cli.StringFlag{
				Name:     "pvc-name",
				EnvVars:  []string{"PVC_NAME"},
				Required: true,
				Usage:    "The PVC name to mount and copy files from.",
			},
			&cli.StringFlag{
				Name:     "directory",
				EnvVars:  []string{"DIRECTORY"},
				Required: true,
				Usage:    "The directory in which to export the OpenTelemetry Collector files.",
			},
		},
		Action: run,
		Authors: []*cli.Author{
			{
				Name:  "Lucas Tesson - PandatiX",
				Email: "lucastesson@protonmail.com",
			},
		},
		Version: version,
		Metadata: map[string]any{
			"version": version,
			"commit":  commit,
			"date":    date,
			"builtBy": builtBy,
		},
	}

	if err := app.Run(os.Args); err != nil {
		log().Fatal("fatal error",
			zap.Error(err),
		)
		os.Exit(1)
	}
}

func run(c *cli.Context) error {
	// Prepare K8s client
	clientset, config, err := getClient()
	if err != nil {
		return err
	}

	ctx := context.Background()

	// Create Pod and mount PVC
	namespace := c.String("namespace")
	podName := "extractor"

	log().Info("Creating Pod",
		zap.String("name", podName),
		zap.String("namespace", namespace),
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
					Name:    "copy",
					Image:   "library/busybox:1.37.0",
					Command: []string{"/bin/sh", "-c", "--"},
					Args:    []string{"while true; do sleep 30; done;"},
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
							ClaimName: c.String("pvc-name"),
						},
					},
				},
			},
		},
	}, metav1.CreateOptions{}); err != nil {
		return err
	}

	// Wait for it to be Up & Running
	waitForPodReady(ctx, clientset, namespace, podName)

	// Copy files
	out := c.String("directory")
	log().Info("Copying files",
		zap.String("directory", out),
	)
	if err := copyFromPod(ctx, config, clientset, namespace, podName, "copy", "/data", out); err != nil {
		return err
	}

	// Delete Pod
	log().Info("Deleting pod",
		zap.String("name", podName),
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

func waitForPodReady(ctx context.Context, clientset *kubernetes.Clientset, namespace, podName string) {
	wait.PollUntilContextTimeout(ctx, 2*time.Second, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
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

func copyFromPod(ctx context.Context, config *rest.Config, clientset *kubernetes.Clientset, namespace, podName, containerName, podPath, localDir string) error {
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

		target := path.Join(dest, hdr.Name)
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

func log() *zap.Logger {
	loggerOnce.Do(func() {
		logger, _ = zap.NewProduction()
	})
	return logger
}

func ptr[T any](t T) *T {
	return &t
}
