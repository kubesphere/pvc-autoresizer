# NOTICE

This repo is forked from [topolvm/pvc-autoresizer](https://github.com/topolvm/pvc-autoresizer) .This one adds storage class level control and automatic restart of workloads on top of that.
# pvc-autoresizer

`pvc-autoresizer` resizes PersistentVolumeClaims (PVCs) when the free amount of storage is below the threshold.
It queries the volume usage metrics from Prometheus that collects metrics from `kubelet`.

**Status**: beta

## Target CSI Drivers

`pvc-autoresizer` supports CSI Drivers that meet the following requirements:

- implement [Volume Expansion](https://kubernetes-csi.github.io/docs/volume-expansion.html).
- implement [NodeGetVolumeStats](https://github.com/container-storage-interface/spec/blob/master/spec.md#nodegetvolumestats)

## Prepare

`pvc-autoresizer` behaves based on the metrics that prometheus collects from kubelet.

Please refer to the following pages to set up Prometheus:

- [Installation | Prometheus](https://prometheus.io/docs/prometheus/latest/installation/)

## Installation

Specify the Prometheus URL to `pvc-autoresizer` argument as `--prometheus-url`.

`pvc-autoresizer` can be deployed to a Kubernetes cluster via `helm`:

```sh
helm install --create-namespace --namespace pvc-autoresizer pvc-autoresizer pvc-autoresizer/pvc-autoresizer --set "controller.args.prometheusURL=<YOUR PROMETHEUS ENDPOINT>"
```

See the Chart [README.md](https://github.com/kubesphere/helm-charts/tree/master/src/main/pvc-autoresizer) for detailed documentation on the Helm Chart.

## How to use
### pvc-autoresize
To allow auto volume expansion, the StorageClass of PVC need to allow volume expansion and
have `resize.kubesphere.io/enabled: "true"` annotation.  The annotation may be omitted if
you give `--no-annotation-check` command-line flag to `pvc-autoresizer` executable.

```yaml
kind: StorageClass
apiVersion: storage.k8s.io/v1
metadata:
  name: csi-qingcloud
  annotations:
    resize.kubesphere.io/enabled: "true"
provisioner: csi-qingcloud
allowVolumeExpansion: true
```

To allow auto volume expansion, the PVC to be resized need to specify the upper limit of
volume size with the annotation `resize.kubesphere.io/storage_limit`. The PVC must have `volumeMode: Filesystem` too.
You can also set the upper limit of volume size with `.spec.resources.limits.storage`, but it is deprecated. If both are present, the annotation takes precedence.

```yaml
kind: PersistentVolumeClaim
apiVersion: v1
metadata:
  name: test-pvc
  namespace: default
  annotations:
    resize.kubesphere.io/storage-limit: 100Gi
spec:
  accessModes:
  - ReadWriteOnce
  volumeMode: Filesystem
  resources:
    requests:
      storage: 30Gi
  storageClassName: csi-qingcloud
```

The PVC can optionally have `resize.kubesphere.io/threshold` and `resize.kubepshere.io/increase` annotations.
(If they are not given, the default value is `10%`.)

When the amount of free space of the volume is below `resize.kubesphere.io/threshold`,
`.spec.resources.requests.storage` is increased by `resize.kubesphere.io/increase`.

If `resize.kubesphere.io/increase` is given as a percentage, the value is calculated as
the current `spec.resources.requests.storage` value multiplied by the annotation value.

```yaml
kind: PersistentVolumeClaim
apiVersion: v1
metadata:
  name: kubesphere-pvc
  namespace: default
  annotations:
    resize.kubesphere.io/storage-limit: 100Gi
    resize.kubesphere.io/threshold: 20%
    resize.kubesphere.io/increase: 20Gi
spec:
  <snip>
```

### workload-autoRestart
The restarter judges the workload that needs to be restarted automatically by checking the status of pvc, and the restarter will stop the workload after successful expansion or timeout and then turn it on again.
If you have a storage class that only supports offline expansion, and need to automatically complete the expansion, you can add the following annotations:
```yaml
kind: StorageClass
apiVersion: storage.k8s.io/v1
metadata:
  name: csi-qingcloud
  labels:
    app.kubernetes.io/managed-by: Helm
  annotations:
    restart.kubesphere.io/enabled: 'true'
    restart.kubesphere.io/online-expansion-support: 'false'
    restart.kubesphere.io/max-time: '300'
```
The workload-autoRestart will run when `restart.kubesphere.io/enabled` is *true* and `restart.kubesphere.io/online-expansion-support` is *false*.
`restart.kubesphere.io/max-time` defines the maximum number of seconds that can be waited for restarting a workload. If the time is exceeded and the restart is not successful, the workload will be skipped afterwards.


Add the following annotations to Deployment or StatefulSet that do not require automatic restart:
```yaml
kind: 
apiVersion: apps/v1
metadata:
  name: kubesphere-workload
  namespace: default
  annotations:
    restart.kubesphere.io/skip: "true"
```
## Container images

Container images are available on [Dockerhub](https://hub.docker.com/repository/docker/kubespheredev/pvc-autoresizer)
