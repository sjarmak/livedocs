# k8s.io/client-go/tools/cache

> Tier 1 Target Documentation — Claims-backed structural output

## Package Summary

**Import path:** `k8s.io/client-go/tools/cache`
**Source files:** 26 (non-test), 28 (test)
**Language:** Go

Client-side caching mechanism for reducing Kubernetes API server calls. Provides Reflector (watches server, updates a Store), multiple Store implementations (simple cache, FIFO queue, DeltaFIFO), and SharedInformer for shared watch multiplexing.

## Exported Interfaces (11)

| Interface             | Methods                                                                                                     | Key Implementations                                                             |
| --------------------- | ----------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------- |
| `Store`               | Add, Update, Delete, List, ListKeys, Get, GetByKey, Replace, Resync, Bookmark, LastStoreSyncResourceVersion | `*cache` (via `NewStore`), `ExpirationCache`, `UndeltaStore`, `FakeCustomStore` |
| `Indexer`             | (embeds Store) + Index, IndexKeys, ListIndexFuncValues, ByIndex, GetIndexers, AddIndexers                   | `*cache` (via `NewIndexer`)                                                     |
| `Queue`               | (embeds Store) + Pop, HasSynced, Close                                                                      | `FIFO`, `DeltaFIFO`, `RealFIFO`                                                 |
| `Controller`          | Run, RunWithContext, HasSynced, LastSyncResourceVersion                                                     | `*controller` (via `New`)                                                       |
| `SharedInformer`      | AddEventHandler, AddEventHandlerWithOptions, GetStore, GetController, HasSynced, ...                        | `*sharedIndexInformer`                                                          |
| `SharedIndexInformer` | (embeds SharedInformer) + AddIndexers, GetIndexer                                                           | `*sharedIndexInformer`                                                          |
| `ListerWatcher`       | List, Watch                                                                                                 | `ListWatch`                                                                     |
| `GenericLister`       | List, ByNamespace, Get                                                                                      | via `NewGenericLister`                                                          |
| `MutationCache`       | GetByKey, ByIndex, Mutation                                                                                 | via `NewIntegerResourceVersionMutationCache`                                    |
| `TransformingStore`   | Transformer                                                                                                 | `DeltaFIFO`                                                                     |
| `DoneChecker`         | Name, Done                                                                                                  | `DeltaFIFO`, `FIFO`                                                             |

## Interface Satisfaction (compiler-verified)

These are confirmed via `var _ Interface = &Type{}` assertions or `go/types`:

- `DeltaFIFO` implements `TransformingStore`, `DoneChecker`
- `FIFO` implements `DoneChecker`
- `*cache` implements `Indexer` (which embeds `Store`)
- `*controller` implements `Controller`
- `*sharedIndexInformer` implements `SharedIndexInformer`
- `ListWatch` satisfies `ListerWatcher`, `ListerWatcherWithContext`
- `ExpirationCache` implements `Store`

## Dependency Graph (k8s.io imports only)

**Direct dependencies (this package imports):**

- `k8s.io/apimachinery/pkg/runtime` — Object type for list/watch
- `k8s.io/apimachinery/pkg/runtime/schema` — GroupResource for listers
- `k8s.io/apimachinery/pkg/api/errors` — NotFound errors in listers
- `k8s.io/apimachinery/pkg/api/meta` — accessor for metadata
- `k8s.io/apimachinery/pkg/apis/meta/v1` — ListOptions
- `k8s.io/apimachinery/pkg/labels` — label selectors
- `k8s.io/apimachinery/pkg/fields` — field selectors
- `k8s.io/apimachinery/pkg/watch` — watch.Interface
- `k8s.io/apimachinery/pkg/util/sets` — set operations
- `k8s.io/apimachinery/pkg/util/wait` — polling/backoff
- `k8s.io/client-go/rest` — RESTClient for ListWatch
- `k8s.io/client-go/features` — feature gates
- `k8s.io/klog/v2` — structured logging
- `k8s.io/utils/clock` — mockable time
- `k8s.io/utils/trace` — latency tracing

**Reverse dependencies (imported by):** 431 packages across kubernetes org

## Key Type Relationships

```
Store ← Indexer ← SharedInformer ← SharedIndexInformer
Store ← Queue ← DeltaFIFO, FIFO, RealFIFO
ListerWatcher → Reflector → Store
Controller = Reflector + Queue + ProcessFunc
SharedIndexInformer = Reflector + Indexer + event distribution
```

## Exported Functions by Category

**Informer constructors (7):** NewInformer, NewInformerWithOptions, NewIndexerInformer, NewTransformingInformer, NewTransformingIndexerInformer, NewSharedInformer, NewSharedIndexInformer, NewSharedIndexInformerWithOptions

**Store constructors (7):** NewStore, NewIndexer, NewTTLStore, NewExpirationStore, NewFakeExpirationStore, NewThreadSafeStore, NewUndeltaStore

**Queue constructors (4):** NewFIFO, NewDeltaFIFO, NewDeltaFIFOWithOptions, NewRealFIFO, NewRealFIFOWithOptions

**Key functions (4):** MetaNamespaceKeyFunc, DeletionHandlingMetaNamespaceKeyFunc, MetaNamespaceIndexFunc, SplitMetaNamespaceKey

**Sync utilities (4):** WaitForCacheSync, WaitForNamedCacheSync, WaitForNamedCacheSyncWithContext, WaitFor

## Test Coverage

28 test files covering controller benchmarks, delta_fifo, expiration_cache, fifo, heap, identity, index, listers, listwatch, mutation_cache, mutation_detector, reflector, shared_informer, store, thread_safe_store.
