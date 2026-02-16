# Inference Operator

A Kubernetes operator for optimized LLM/ML model inference hosting through intelligent GPU resource management, multi-tier token caching, and support for multiple inference backends.

## Features

- **Multiple Inference Backends**: Support for vLLM, Triton Inference Server, and TensorRT-LLM
- **GPU Resource Management**:
  - MIG (Multi-Instance GPU) partitioning
  - MPS (Multi-Process Service) sharing
  - Time-slicing scheduler
  - GPU pool management with oversubscription
- **Multi-Tier Token Caching**:
  - GPU prefix cache for KV tensors
  - Local NVMe cache with compression
  - Distributed Redis cache
  - Cache warmup and eviction policies
- **Autoscaling**: Custom metrics-based autoscaling (GPU utilization, queue depth, latency)
- **Observability**: Prometheus metrics, health checks, and status reporting

## Prerequisites

- Kubernetes 1.28+
- NVIDIA GPU Operator (for GPU workloads)
- kubectl
- Go 1.22+ (for development)

## Installation

### Install CRDs

```bash
make install
```

### Deploy the Operator

```bash
make deploy
```

### Verify Installation

```bash
kubectl get pods -n inference-system
kubectl get crd | grep inference.ai
```

## Custom Resource Definitions

### InferenceService

Deploy and manage ML model inference services.

```yaml
apiVersion: inference.ai/v1alpha1
kind: InferenceService
metadata:
  name: llama-70b
spec:
  model:
    name: meta-llama/Llama-2-70b
    source: huggingface
  backend: vllm
  replicas: 3
  gpu:
    type: nvidia-a100
    count: 2
    sharing: mig
    migProfile: 3g.40gb
  cache:
    enabled: true
    strategy: hybrid
    prefixCacheSize: 8Gi
  autoscaling:
    minReplicas: 1
    maxReplicas: 10
    metrics:
      - type: gpu-utilization
        target: 80
```

### GPUPool

Manage GPU resources across your cluster.

```yaml
apiVersion: inference.ai/v1alpha1
kind: GPUPool
metadata:
  name: a100-pool
spec:
  nodeSelector:
    gpu-type: nvidia-a100
  sharing:
    mode: mig
    profiles:
      - name: 3g.40gb
        count: 2
      - name: 1g.10gb
        count: 4
  oversubscription:
    enabled: true
    ratio: "1.5"
```

### TokenCache

Configure multi-tier caching for token/KV cache data.

```yaml
apiVersion: inference.ai/v1alpha1
kind: TokenCache
metadata:
  name: shared-cache
spec:
  tiers:
    - name: gpu-prefix
      type: prefix
      size: 16Gi
      ttl: 1h
    - name: local-nvme
      type: local
      size: 100Gi
      ttl: 24h
    - name: distributed
      type: redis
      replicas: 3
      size: 500Gi
      ttl: 168h
  evictionPolicy: lru
  warmup:
    enabled: true
    commonPrefixes:
      - "You are a helpful assistant"
```

## Usage Examples

### Deploy a Model with vLLM

```bash
# Create HuggingFace token secret
kubectl create secret generic huggingface-token \
  --from-literal=token=hf_xxxxx

# Deploy the inference service
kubectl apply -f config/samples/inference_v1alpha1_inferenceservice.yaml

# Check status
kubectl get inferenceservices
kubectl describe inferenceservice llama-70b
```

### Create a GPU Pool with MIG

```bash
kubectl apply -f config/samples/inference_v1alpha1_gpupool.yaml
kubectl get gpupools
```

### Configure Token Caching

```bash
kubectl apply -f config/samples/inference_v1alpha1_tokencache.yaml
kubectl get tokencaches
```

## Development

### Build

```bash
make build
```

### Run Locally

```bash
make run
```

### Run Tests

```bash
make test
make test-e2e
```

### Build Docker Image

```bash
make docker-build IMG=inference-operator:dev
```

### Deploy to Kind Cluster

```bash
make kind-create
make deploy-kind
make deploy-samples
```

## Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                    Inference Operator                            │
├─────────────────────────────────────────────────────────────────┤
│  ┌─────────────────┐ ┌─────────────────┐ ┌─────────────────┐   │
│  │ InferenceService │ │    GPUPool      │ │   TokenCache    │   │
│  │   Controller     │ │   Controller    │ │   Controller    │   │
│  └────────┬────────┘ └────────┬────────┘ └────────┬────────┘   │
│           │                   │                    │            │
│  ┌────────▼──────────────────▼────────────────────▼────────┐   │
│  │                    Shared Components                      │   │
│  │  ┌──────────────┐ ┌──────────────┐ ┌──────────────────┐  │   │
│  │  │  GPU Manager │ │Cache Manager │ │ Backend Factory  │  │   │
│  │  │  - MIG       │ │  - Prefix    │ │  - vLLM          │  │   │
│  │  │  - MPS       │ │  - Local     │ │  - Triton        │  │   │
│  │  │  - Timeslice │ │  - Redis     │ │  - TensorRT-LLM  │  │   │
│  │  └──────────────┘ └──────────────┘ └──────────────────┘  │   │
│  └──────────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────────┘
```

## GPU Sharing Modes

| Mode | Description | Use Case |
|------|-------------|----------|
| `exclusive` | One workload per GPU | Maximum performance |
| `mig` | Hardware partitioning (A100/H100) | Guaranteed isolation |
| `mps` | Process sharing with CUDA MPS | Concurrent workloads |
| `timeslice` | Software time-slicing | Development/testing |

## Metrics

The operator exposes Prometheus metrics on port 8080:

- `inference_gpu_utilization` - GPU utilization percentage
- `inference_gpu_memory_used_bytes` - GPU memory usage
- `inference_cache_hits_total` - Cache hit count
- `inference_cache_size_bytes` - Cache size
- `inference_gpu_allocations_total` - GPU allocation count

## Configuration

### Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `METRICS_BIND_ADDRESS` | Metrics endpoint address | `:8080` |
| `HEALTH_PROBE_BIND_ADDRESS` | Health probe address | `:8081` |
| `LEADER_ELECT` | Enable leader election | `false` |

## Troubleshooting

### Check Operator Logs

```bash
kubectl logs -n inference-system deployment/inference-operator-controller-manager
```

### Check InferenceService Status

```bash
kubectl describe inferenceservice <name>
kubectl get events --field-selector involvedObject.name=<name>
```

### Common Issues

1. **GPU not detected**: Ensure NVIDIA GPU Operator is installed
2. **Model download fails**: Check HuggingFace token secret
3. **Pod pending**: Check GPU resource availability

## License

Apache License 2.0
