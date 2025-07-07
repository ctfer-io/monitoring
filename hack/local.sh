#!/bin/bash

export PULUMI_CONFIG_PASSPHRASE=""
pulumi login --local

# Delete previous iterations
kind delete cluster | true
pulumi stack rm tmp --force --yes
docker stop registry && docker rm registry

# Create local registry (speed up downloads by reusing locals)
docker run -d --restart=always -p 5000:5000 --name registry registry:2

# Run Kind and connect registry
cat <<EOF > kind-config.yaml
apiVersion: kind.x-k8s.io/v1alpha4
kind: Cluster
containerdConfigPatches:
- |
  [plugins."io.containerd.grpc.v1.cri".registry.mirrors."localhost:5000"]
    endpoint = ["http://registry:5000"]

kubeadmConfigPatches:
- |
  kind: ClusterConfiguration
  apiServer:
    extraArgs:
      "service-node-port-range": "30000-30005"

nodes:
- role: control-plane
  extraPortMappings:
  - containerPort: 30000
    hostPort: 30000
  - containerPort: 30001
    hostPort: 30001
  - containerPort: 30002
    hostPort: 30002
  - containerPort: 30003
    hostPort: 30003
  - containerPort: 30004
    hostPort: 30004
  - containerPort: 30005
    hostPort: 30005
EOF
kind create cluster --config=kind-config.yaml
docker network connect kind registry

# Enable storageclass "standard" RWO/RWX
# From https://github.com/kubernetes-sigs/kind/issues/1487#issuecomment-2211072952
kubectl -n local-path-storage patch configmap local-path-config -p '{"data": {"config.json": "{\n\"sharedFileSystemPath\": \"/var/local-path-provisioner\"\n}"}}'

# Load images
docker pull jaegertracing/jaeger:2.8.0 && docker tag $_ localhost:5000/$_ && docker push $_
docker pull otel/opentelemetry-collector-contrib:0.129.1 && docker tag $_ localhost:5000/$_ && docker push $_
docker pull prom/prometheus:v3.4.2 && docker tag $_ localhost:5000/$_ && docker push $_

# Deploy Monitoring
pulumi stack init tmp
pulumi config set cold-extract true
pulumi config set registry localhost:5000
pulumi up -y
