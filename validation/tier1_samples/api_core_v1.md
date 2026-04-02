# k8s.io/api/core/v1

> Tier 1 Target Documentation — Claims-backed structural output

## Package Summary

**Import path:** `k8s.io/api/core/v1`
**Source files:** 11 (non-generated, non-test)
**Primary file:** `types.go` (8529 lines)
**Language:** Go

The v1 version of the Kubernetes core API. Defines the struct types for all core resources (Pod, Service, Node, Namespace, ConfigMap, Secret, etc.) and their associated enums, constants, and helper methods.

## Exported Types by Category

**Workload resources (top-level API objects):**
Pod, PodList, PodTemplate, PodTemplateList, ReplicationController, ReplicationControllerList

**Service/networking:**
Service, ServiceList, Endpoints, EndpointsList

**Configuration:**
ConfigMap, ConfigMapList, Secret, SecretList

**Storage:**
PersistentVolume, PersistentVolumeList, PersistentVolumeClaim, PersistentVolumeClaimList

**Cluster:**
Node, NodeList, Namespace, NamespaceList, ComponentStatus, ComponentStatusList, Event, EventList

**Volume sources (30+):**
AWSElasticBlockStoreVolumeSource, AzureDiskVolumeSource, CSIVolumeSource, EmptyDirVolumeSource, HostPathVolumeSource, NFSVolumeSource, PersistentVolumeClaimVolumeSource, ... (and many more)

**Pod spec types:**
Container, EphemeralContainer, ContainerPort, ContainerStatus, EnvVar, EnvVarSource, VolumeMount, Probe, Lifecycle, SecurityContext, ResourceRequirements, ...

## Constants

~100 exported constants including:

- Resource names: `ResourcePods`, `ResourceCPU`, `ResourceMemory`, ...
- Event types: `EventTypeNormal`, `EventTypeWarning`
- Secret types: `SecretTypeOpaque`, `SecretTypeDockerConfigJson`, ...
- DNS policies: `DNSClusterFirst`, `DNSDefault`, ...
- Service types: `ServiceTypeClusterIP`, `ServiceTypeNodePort`, `ServiceTypeLoadBalancer`
- Node conditions: `NodeReady`, `NodeMemoryPressure`, `NodeDiskPressure`, ...
- Taint keys: `TaintNodeNotReady`, `TaintNodeUnreachable`, ...

## Dependency Graph (k8s.io imports only)

**Direct dependencies:**

- `k8s.io/apimachinery/pkg/api/resource` — Quantity type for resource limits
- `k8s.io/apimachinery/pkg/apis/meta/v1` — ObjectMeta, TypeMeta, ListMeta
- `k8s.io/apimachinery/pkg/types` — UID, NamespacedName
- `k8s.io/apimachinery/pkg/util/intstr` — IntOrString for ports
- `k8s.io/apimachinery/pkg/runtime` — Scheme registration
- `k8s.io/apimachinery/pkg/runtime/schema` — GroupVersion, GroupResource
- `k8s.io/klog/v2` — logging (used in toleration.go)

**Reverse dependencies:** 576 packages across kubernetes org (most-imported k8s package)

## Schema Registration

Registered via `SchemeBuilder` and `addKnownTypes` into the `v1` GroupVersion. All top-level types (Pod, Service, Node, etc.) implement `runtime.Object` via embedded `TypeMeta` and generated DeepCopy methods.

## Generated Code

This package has substantial generated code:

- `zz_generated.deepcopy.go` — DeepCopyObject/DeepCopyInto for all types
- `generated.pb.go` — protobuf serialization
- `types_swagger_doc_generated.go` — swagger documentation strings

## Notes

This is a pure type-definition package with almost no logic. The types.go file alone is 8529 lines. The primary value for developers is understanding which types exist, their field structures, and which constants/enums are available — all of which `go doc` already provides well.
