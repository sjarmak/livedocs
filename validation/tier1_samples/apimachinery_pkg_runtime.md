# k8s.io/apimachinery/pkg/runtime

> Tier 1 Target Documentation — Claims-backed structural output

## Package Summary

**Import path:** `k8s.io/apimachinery/pkg/runtime`
**Language:** Go

Defines the core type system for Kubernetes API objects: conversions between versions, serialization/deserialization, and the Scheme registry that maps Go types to GroupVersionKinds. Every API object implements `runtime.Object`. This is the foundational machinery that makes Kubernetes multi-version API possible.

## Core Interfaces

| Interface                    | Purpose                                                                | Key Implementations                                   |
| ---------------------------- | ---------------------------------------------------------------------- | ----------------------------------------------------- |
| `Object`                     | All API objects: GetObjectKind, DeepCopyObject                         | Every k8s resource type (Pod, Service, etc.)          |
| `Scheme`                     | Type registry: maps Go types ↔ GVKs, handles conversion and defaulting | Singleton via `NewScheme()`                           |
| `Codec` / `Serializer`       | Encode/Decode Objects to/from bytes                                    | JSON, YAML, Protobuf, CBOR serializers                |
| `Encoder`                    | Object → bytes                                                         | JSON encoder, protobuf encoder, WithVersionEncoder    |
| `Decoder`                    | bytes → Object                                                         | JSON decoder, protobuf decoder, WithoutVersionDecoder |
| `NegotiatedSerializer`       | Content-type negotiation for serializers                               | `NewSimpleNegotiatedSerializer`                       |
| `ObjectTyper`                | Determine GVK from Go type                                             | `Scheme`                                              |
| `ObjectCreater`              | Instantiate Go type from GVK                                           | `Scheme`                                              |
| `ObjectConvertor`            | Convert between API versions                                           | `Scheme`, `UnsafeObjectConvertor`                     |
| `ObjectDefaulter`            | Apply defaults to API objects                                          | `Scheme`                                              |
| `GroupVersioner`             | GVK selection strategy                                                 | `InternalGroupVersioner`, `NewMultiGroupVersioner`    |
| `Unstructured`               | map[string]interface{} API object (no Go struct)                       | `unstructured.Unstructured` (in separate pkg)         |
| `ParameterCodec`             | Query string ↔ versioned options                                       | `NewParameterCodec`                                   |
| `EquivalentResourceRegistry` | Track equivalent resources across versions                             | `NewEquivalentResourceRegistry`                       |

## Interface Satisfaction

- `Scheme` implements `ObjectTyper`, `ObjectCreater`, `ObjectConvertor`, `ObjectDefaulter`
- `WithVersionEncoder` implements `Encoder`
- `WithoutVersionDecoder` implements `Decoder`
- `NoopEncoder` implements `Encoder`
- `NoopDecoder` implements `Decoder`
- `Unknown` implements `Object`
- `TypeMeta` provides `GetObjectKind()` for `Object`

## Dependency Graph (k8s.io imports only)

**Direct dependencies:**

- `k8s.io/apimachinery/pkg/runtime/schema` — GroupVersionKind, GroupVersion
- `k8s.io/apimachinery/pkg/conversion` — conversion framework
- `k8s.io/apimachinery/pkg/conversion/queryparams` — URL query ↔ struct
- `k8s.io/apimachinery/pkg/util/json` — JSON utilities
- `k8s.io/apimachinery/pkg/util/sets` — set operations
- `k8s.io/apimachinery/pkg/util/naming` — scheme naming
- `k8s.io/apimachinery/pkg/util/runtime` — panic recovery
- `k8s.io/apimachinery/pkg/util/errors` — error aggregation
- `k8s.io/apimachinery/pkg/util/validation/field` — field path errors
- `k8s.io/apimachinery/pkg/api/operation` — operation context
- `k8s.io/apimachinery/pkg/runtime/serializer/cbor/direct` — CBOR encoding
- `k8s.io/kube-openapi/pkg/util` — OpenAPI utilities
- `k8s.io/klog/v2` — structured logging

**Reverse dependencies:** 1224 packages (most-depended-on infrastructure package)

## Key Architectural Pattern

```
Go types ──→ Scheme.AddKnownTypes() ──→ Scheme registry
                                            │
Client request ──→ NegotiatedSerializer ──→ Decoder ──→ Object (internal)
                                            │
Object (internal) ──→ Scheme.Convert() ──→ Object (versioned) ──→ Encoder ──→ wire format
```

## Exported Functions by Category

**Codec helpers (6):** Encode, EncodeList, EncodeOrDie, Decode, DecodeInto, DecodeList, CheckCodec

**Error constructors (8):** NewMissingKindErr, NewMissingVersionErr, NewNotRegisteredErrForKind, NewNotRegisteredErrForType, NewNotRegisteredErrForTarget, NewNotRegisteredGVKErrForTarget, NewStrictDecodingError, IsMissingKind, IsMissingVersion, IsNotRegisteredError, IsStrictDecodingError

**Conversion helpers (11):** Convert_Slice_string_To_bool, Convert_Slice_string_To_int, ... (query param conversion utilities)

**Scheme setup:** NewScheme, NewSchemeBuilder, RegisterEmbeddedConversions, RegisterStringConversions
