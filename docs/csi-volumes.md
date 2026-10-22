# CSI Volumes for Actors in Agent Substrate

Substrate integrates with the **Container Storage Interface (CSI)** to provide dynamically provisioned, per-actor external volumes that seamlessly attach and detach as actors transition through their lifecycle.

External volumes are a preview feature. The ate API server accepts them only
when it runs with `--preview=ExternalVolumes` (ate-setup's `preview` setting)
and the client opts in to preview features: `kubectl ate --preview`, or the
`Ate-Preview-Allowed: true` gRPC metadata header. The opt-in is needed to
create an actor template with external volumes, or an actor from such a
template. Later operations on that actor, such as resume, suspend and delete,
do not need it.

---

## 1. CSI in Substrate vs. Standard Kubernetes

In Kubernetes, volumes are reconciled asynchronously via standard Kubernetes objects (e.g. `PersistentVolumeClaim`, `PersistentVolume`). Agent Substrate takes a different approach tailored for actor lifecycle operations:

* **No PV or PVC Objects:** External volumes are declaratively defined in the [`ActorTemplate`](api-guide.md#2-actortemplate-the-workload-blueprint) via `external.template` and provisioned dynamically for each actor instance. Volume operations are coupled directly with the actor lifecycle.
* **Direct Network-Based CSI Controller:** The Substrate control plane (`ateapi`) communicates directly with the CSI Controller gRPC service over the network (via TCP or DNS endpoints, optionally secured with TLS/mTLS).

---

## 2. Dynamic CSI Driver Discovery (`CSIDriverConfig`)

To discover and communicate with CSI drivers, Substrate uses dynamic discovery driven by the cluster-scoped **`CSIDriverConfig`** Custom Resource Definition (CRD).

### The `CSIDriverConfig` Resource

`CSIDriverConfig` defines the gRPC connection parameters for a specific CSI driver. It bridges the Kubernetes `StorageClass` (referenced in the `ActorTemplate`) to the network endpoint of the CSI Controller service and the local socket path of the CSI Node plugin.

```yaml
apiVersion: ate.dev/v1alpha1
kind: CSIDriverConfig
metadata:
  name: nfs.csi.k8s.io
spec:
  driverName: nfs.csi.k8s.io
  controllerEndpoint: tcp://csi-nfs-controller.kube-system.svc.cluster.local:50052
  nodeSocketOverride: unix:///var/lib/kubelet/plugins/csi-nfsplugin/csi.sock
  tls:
    enabled: true
    usePodIdentity: true
    serverName: csi-nfs-controller.kube-system.svc.cluster.local
```

### Specification (`CSIDriverConfigSpec`)

| Field | Type | Description |
| :--- | :--- | :--- |
| `driverName` | `string` | **Required.** The standard CSI driver name (e.g. `nfs.csi.k8s.io`, `hostpath.csi.k8s.io`, `pd.csi.storage.gke.io`). Matches the `provisioner` field on the referenced Kubernetes `StorageClass`. |
| `controllerEndpoint` | `string` | **Required.** The gRPC endpoint for the CSI Controller service. Must be a valid URI starting with `tcp://`, `dns:///`, or `unix://` (e.g., `tcp://csi-controller.kube-system.svc:50051` or `dns:///csi-svc.default.svc:9000`). |
| `nodeSocketOverride` | `string` | **Optional.** Override for the CSI Node service Unix domain socket on worker nodes. Must begin with `unix://`. If omitted, Substrate defaults to `unix:///var/lib/kubelet/plugins/<driverName>/csi.sock`. |
| `tls` | `*CSIDriverTLSConfig` | **Optional.** Configures TLS or mTLS for the gRPC connection to the `controllerEndpoint`. |

#### TLS / mTLS Configuration (`spec.tls`)

| Field | Type | Description |
| :--- | :--- | :--- |
| `enabled` | `bool` | **Required.** Enables TLS/mTLS for the gRPC connection. |
| `usePodIdentity` | `bool` | **Optional.** When `true`, reuses Substrate's SPIFFE Pod Identity certificates for mutual TLS (mTLS) with dynamic CA trust bundle verification and rotation. Must be `true` when `enabled` is `true`. |
| `serverName` | `string` | **Required.** Expected server name (SAN) for TLS certificate verification when TLS is enabled. |

> [!NOTE]
> For details on exposing CSI controller endpoints over the network and configuring CSI node DaemonSets with required mount propagations, see the [CSI Driver Deployment Guide](csi-deployment.md).

---

## 3. ActorTemplate: Configuring CSI Volumes

External volumes are declared on the `ActorTemplate` resource. For complete details on actor templates, see the [ActorTemplate: The Workload Blueprint](api-guide.md#2-actortemplate-the-workload-blueprint) section in the Substrate API Guide.

### Volume Configuration Fields

To attach a CSI volume to an actor:

1. Define the volume under `volumes` with an `external.template`.
2. Mount the volume inside one or more containers under `containers[].volumeMounts`.

#### `volumes[]`

```yaml
volumes:
- name: my-data-volume
  external:
    template:
      capacity: 10Gi
      storageClassName: standard-rwx
```

* `name`: Unique DNS-label-compliant volume name.
* `external.template.capacity`: Quantity string representing the requested volume size (e.g. `1Gi`, `50Gi`).
* `external.template.storageClassName`: Name of a Kubernetes `StorageClass` present in the cluster whose `provisioner` matches a registered `CSIDriverConfig`.

#### `containers[].volumeMounts[]`

```yaml
volumeMounts:
- name: my-data-volume
  mountPath: /var/data
```

* `name`: Must match the declared `volumes[].name`.
* `mountPath`: Unix path inside the container sandbox where the volume will be mounted.

> [!NOTE]
> All declared volumes in `volumes` must be mounted by at least one container.

---

## 4. End-to-End Example

The following example demonstrates setting up an NFS CSI driver with Substrate and deploying an `ActorTemplate` that mounts an external NFS volume.

### Step 1: Create the StorageClass

```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: csi-nfs-sc
provisioner: nfs.csi.k8s.io
parameters:
  server: nfs-server.default.svc.cluster.local
  share: /
reclaimPolicy: Delete
volumeBindingMode: Immediate
mountOptions:
  - nfsvers=4.1
```

### Step 2: Register the CSIDriverConfig

```yaml
apiVersion: ate.dev/v1alpha1
kind: CSIDriverConfig
metadata:
  name: nfs.csi.k8s.io
spec:
  driverName: nfs.csi.k8s.io
  controllerEndpoint: tcp://csi-nfs-controller.kube-system.svc.cluster.local:50052
  nodeSocketOverride: unix:///var/lib/kubelet/plugins/csi-nfsplugin/csi.sock
  tls:
    enabled: true
    usePodIdentity: true
    serverName: csi-nfs-controller.kube-system.svc.cluster.local
```

### Step 3: Define WorkerPool and ActorTemplate

Refer to [ActorTemplate: The Workload Blueprint](api-guide.md#2-actortemplate-the-workload-blueprint) for general template options.

The `WorkerPool` is a Kubernetes resource, applied with `kubectl apply`:

```yaml
apiVersion: ate.dev/v1alpha1
kind: WorkerPool
metadata:
  name: agent-pool
  namespace: ate-demo
  labels:
    workload: stateful-agent
spec:
  replicas: 5
  workerImage: ko://go.ate.dev/substrate/cmd/ateworker-gvisor
  sandboxClasses:
  - name: gvisor
```

The `ActorTemplate` is a protojson-shaped `ateapipb.ActorTemplate`, created
through the ate API with `kubectl ate --preview create actor-template -f -` (the
`ate-demo` atespace must exist):

```yaml
metadata:
  atespace: ate-demo
  name: stateful-agent-template
workerSelector:
  matchLabels:
    workload: stateful-agent
containers:
- name: agent
  image: gcr.io/my-project/agent-app@sha256:7f28ab0...
  volumeMounts:
  - name: shared-storage
    mountPath: /mnt/shared
  wakeupProbe:
    httpGet:
      path: /readyz
      port: 8080
resources:
  limits:
  - name: cpu
    quantity: "1"
  - name: memory
    quantity: 1Gi
snapshotConfig:
  storageLocation: gs://my-snapshots-bucket/stateful-agent
volumes:
- name: shared-storage
  external:
    template:
      capacity: 5Gi
      storageClassName: csi-nfs-sc
```

## 5. Shared Volumes

A `SharedVolume` is an atespace-scoped CSI volume whose lifetime is
independent of any actor, so several actors can mount the same storage in turn or concurrently.
Create one from a StorageClass, or register a volume that already exists:

```bash
kubectl ate create sharedvolume ctx-42 -a demo --storage-class csi-nfs-sc --capacity 10Gi
kubectl ate create sharedvolume imported -a demo --volume-id <csi-volume-id> --volume-type nfs.csi.k8s.io
```

For concurrent actors, create the volume with `--access-mode rwx`:

```bash
kubectl ate create sharedvolume workspace -a demo --storage-class csi-nfs-sc --capacity 10Gi --access-mode rwx
```

RWX currently requires a CSI driver that advertises neither controller
`PUBLISH_UNPUBLISH_VOLUME` nor node `STAGE_UNSTAGE_VOLUME`. Capability discovery
must succeed. Drivers requiring attachment or staging are rejected before those
operations run; they need persistent per-node reference tracking before they can
be used safely with concurrent actors. Provisioning checks controller capabilities;
resume checks controller and node capabilities, including for registered volumes.

Each actor gets its own node publish target. Suspension or deletion unpublishes
only that target, without detaching or unstaging storage used by other actors.
RWX requests use CSI `MULTI_NODE_MULTI_WRITER`; an incompatible backend may reject
provisioning or publishing. Per-mount `read_only` remains a sandbox mount property.
Writers sharing files must coordinate updates themselves. Separate `sub_path`
views can give each actor its own workspace while sharing a read-only skills tree.
Actor snapshots do not provide a coordinated snapshot of shared external storage;
other actors may change its contents while an actor is suspended.

Each volume records:

- `accessMode`: fixed at create. `READ_WRITE_ONCE` (the default) permits
  one resumed actor; a conflicting resume fails and names the holder.
  `READ_WRITE_MANY` permits multiple resumed actors on the same or different
  nodes. The CSI backend must support concurrent filesystem writers.
- `deleteTrigger`: `MANUAL` removes the volume only on an explicit delete;
  `LAST_ACTOR` removes it when the last actor referencing it is deleted.
  Provisioned volumes default to `LAST_ACTOR`, registered ones to `MANUAL`.
  Deleting the volume also deletes the storage.
- `status.refs`: every actor referencing the volume, with `active` set while
  the actor is resumed. A referenced volume cannot be deleted.

A template mounts a `SharedVolume` with `external.sharedVolume`. Name the volume
in the template, or leave the name empty and bind it per actor at creation:

```yaml
volumes:
- name: shared
  external: {sharedVolume: {}}
```

```bash
kubectl ate create actor producer-42 -a demo --template producer --volume shared=ctx-42
kubectl ate create actor consumer-42 -a demo --template consumer --volume shared=ctx-42
```

Bindings are validated and recorded when the actor is created and cannot change
afterwards.

By default the golden snapshot is not used for templates that reference a
`SharedVolume`. The first resume of such an actor is a cold boot from the image,
and later resumes restore the actor's own snapshot, which was taken over the
real volume. An explicit `sourceTag` is refused at actor creation. The reason is
in gVisor: the golden actor warms up against a local empty directory at the
mount path, but runsc derives the `disable_file_handle_sharing` mount option
from the bind source's filesystem (NFS and FUSE get it, local disks do not) and
refuses a restore whose mount options differ from the checkpoint's.

A template can instead warm its golden actor up on the SharedVolumes
themselves, so that the golden is taken over the same filesystem every actor
mounts:

```yaml
snapshotConfig:
  goldenSnapshotConfig:
    mode: GOLDEN_SNAPSHOT_MODE_ENABLED
    mountSharedVolumes: true
    sharedVolumeSubPaths:
      session: .golden-session
```

With `mountSharedVolumes`, every `external.sharedVolume` in the template must be
named, and every volume the template mounts writable needs an entry in
`sharedVolumeSubPaths`. That entry roots the golden actor's view, as an actor's
`sharedVolumeSubPaths` does, so the golden actor never writes outside it.
Read-only mounts of a volume without an entry use the template's `subPath`.
Actors created from the template then start from the golden tag, and their
first resume restores it over their own view of the volumes.

The golden's application state is restored as it was, over a different
directory. That is only safe when the application holds no file, directory or
mapping open in a shared volume when the golden is taken: such a handle is
reopened by path in the actor's view on restore, where it may not exist or may
be another file. Substrate cannot check this; a template should opt in only for
an application that touches its shared volumes after it has served its first
request, not during warm-up.

### Per-mount views of a shared volume

A `SharedVolume` still identifies the whole CSI volume. On each `volumeMounts`
entry, `subPath` selects a caller-supplied directory and `readOnly` restricts
only that bind. One container can mount the same shared volume read-write at
`/workspace` with `subPath: sessions/018f2c` and read-only at `/skills` with
`subPath: skills`. Both workers use the same confined bind preparation; neither
option changes CSI publication, the shared volume, or another mount's permissions.
See [Volume mount views](api-guide.md#volume-mount-views) for validation and
missing-directory rules.

Golden actors use an empty local placeholder for a shared volume. A selected
read-only directory (or a missing parent of a writable directory) will not exist
there. Disable golden snapshot creation for such templates. These mount options
do not change the shared-volume cold-boot policy or make a placeholder snapshot
restorable onto NFS.
