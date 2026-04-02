# k8s.io/kubernetes/pkg/kubelet/config

> Tier 1 Target Documentation — Claims-backed structural output

## Package Summary

**Import path:** `k8s.io/kubernetes/pkg/kubelet/config`
**Source files:** 10 (non-test), 7 (test)
**Language:** Go

Implements pod configuration readers — the sources from which the kubelet learns about pods it should run. Three sources: API server (primary), static pod file, and static pod URL. PodConfig merges all sources and provides a unified channel of pod updates.

## Exported Types

| Type             | Kind      | Description                                                                   |
| ---------------- | --------- | ----------------------------------------------------------------------------- |
| `PodConfig`      | struct    | Merges pod updates from multiple sources into a single channel                |
| `SourcesReady`   | interface | Reports whether all configured pod sources have delivered at least one update |
| `SourcesReadyFn` | func type | `func(sourcesSeen sets.Set[string]) bool`                                     |

## Exported Functions

| Function             | Signature                                          | Purpose                                             |
| -------------------- | -------------------------------------------------- | --------------------------------------------------- |
| `NewPodConfig`       | `(recorder, observer) *PodConfig`                  | Creates the central pod config merge point          |
| `NewSourceApiserver` | `(logger, client, nodeName, updates)`              | Watches API server for pod assignments to this node |
| `NewSourceFile`      | `(logger, path, nodeName, period, updates)`        | Watches a directory for static pod manifests        |
| `NewSourceURL`       | `(logger, url, header, nodeName, period, updates)` | Polls a URL for static pod manifests                |
| `NewSourcesReady`    | `(fn) SourcesReady`                                | Wraps a readiness function                          |

## Exported Constants and Errors

- `WaitForAPIServerSyncPeriod` = 1 second
- `ErrStaticPodTriedToUseClusterTrustBundle` — static pods cannot use ClusterTrustBundle projected volumes
- `ErrStaticPodTriedToUseResourceClaims` — static pods cannot use ResourceClaims

## Dependency Graph (k8s.io imports only)

**Direct dependencies:**

- `k8s.io/api/core/v1` — Pod types
- `k8s.io/apimachinery/pkg/apis/meta/v1` — ObjectMeta, ListOptions
- `k8s.io/apimachinery/pkg/fields` — field selectors for API watch
- `k8s.io/apimachinery/pkg/types` — NodeName, UID
- `k8s.io/apimachinery/pkg/runtime` — scheme for decoding pod manifests
- `k8s.io/apimachinery/pkg/util/wait` — polling
- `k8s.io/apimachinery/pkg/util/yaml` — YAML decoding for static pods
- `k8s.io/client-go/kubernetes` — clientset for API server source
- `k8s.io/client-go/tools/cache` — ListWatch + Reflector for API server source
- `k8s.io/apiserver/pkg/util/feature` — feature gate checks
- `k8s.io/klog/v2` — structured logging
- `k8s.io/kubernetes/pkg/api/pod` — pod utility functions
- `k8s.io/kubernetes/pkg/apis/core` — internal API types
- `k8s.io/kubernetes/pkg/apis/core/helper` — API helpers
- `k8s.io/kubernetes/pkg/api/legacyscheme` — legacy scheme registration
- `k8s.io/kubernetes/pkg/features` — feature gate constants

**Reverse dependencies (imported by):** 20 packages including:

- `cmd/kubelet/app/server.go` — kubelet startup
- `pkg/kubelet/kubelet.go` — main kubelet logic
- `pkg/kubelet/cm/container_manager*.go` — container manager variants
- `pkg/kubelet/cm/cpumanager/` — CPU manager
- `pkg/kubelet/cm/devicemanager/` — device manager
- `pkg/kubelet/cm/dra/` — dynamic resource allocation
- `pkg/kubelet/cm/memorymanager/` — memory manager
- `pkg/kubelet/volumemanager/` — volume manager
- `pkg/kubelet/pluginmanager/` — plugin manager

## Architecture

```
                  ┌──────────────────┐
                  │    PodConfig     │ (merge point)
                  └──┬──────┬──────┬─┘
                     │      │      │
              ┌──────┘      │      └──────┐
              ▼             ▼             ▼
      SourceApiserver  SourceFile   SourceURL
      (watch API)      (dir poll)  (HTTP poll)
              │             │             │
              └──────┬──────┘─────────────┘
                     ▼
              Updates channel → kubelet main loop
```

## Test Files

7 test files: apiserver_test, common_test, config_test, file_linux_test, file_test, http_test, sources_test.
