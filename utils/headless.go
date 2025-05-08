package utils

import (
	"fmt"

	corev1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/core/v1"
	metav1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/meta/v1"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// Headless return the <name>.<namespace>:<port> record of an
// Kubernetes headless service.
// If requires a single port to be defined as part of this service.
func Headless(svc *corev1.Service) pulumi.StringOutput {
	return pulumi.All(svc.Metadata, svc.Spec).ApplyT(func(all []any) string {
		meta := all[0].(metav1.ObjectMeta)
		spec := all[1].(corev1.ServiceSpec)

		if meta.Name == nil || meta.Namespace == nil || len(spec.Ports) != 1 {
			return ""
		}

		return fmt.Sprintf("%s.%s:%d", *meta.Name, *meta.Namespace, spec.Ports[0].Port)
	}).(pulumi.StringOutput)
}
