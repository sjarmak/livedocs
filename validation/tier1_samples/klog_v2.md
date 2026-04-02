# k8s.io/klog/v2

> Tier 1 Target Documentation — Claims-backed structural output

## Package Summary

**Import path:** `k8s.io/klog/v2`
**Language:** Go

Kubernetes logging library. Wraps go-logr with severity levels, V-leveled logging, structured logging (InfoS/ErrorS), output routing (stderr, files), and a flush daemon. Used by virtually every package in the kubernetes org.

## Exported Types

| Type           | Kind       | Description                                                                  |
| -------------- | ---------- | ---------------------------------------------------------------------------- |
| `Logger`       | type alias | `= logr.Logger` — the primary logger interface                               |
| `LogSink`      | type alias | `= logr.LogSink` — backend for Logger                                        |
| `Level`        | int32      | Verbosity level for V-leveled logging                                        |
| `Verbose`      | struct     | Returned by `V()`, has `Info`, `Infof`, `Infoln`, `InfoS`, `Enabled` methods |
| `ObjectRef`    | struct     | Namespace + Name for structured log references                               |
| `KMetadata`    | interface  | Object with GetName/GetNamespace for KObj                                    |
| `LogFilter`    | interface  | Deprecated filter for log messages                                           |
| `OutputStats`  | struct     | Byte counts per severity level                                               |
| `State`        | interface  | Captured klog state for save/restore                                         |
| `LoggerOption` | func type  | Options for SetLoggerWithOptions                                             |

## Exported Functions by Category

**Unstructured logging (severity-leveled):**

- Info, Infof, Infoln, InfoDepth, InfofDepth, InfolnDepth
- Warning, Warningf, Warningln, WarningDepth, WarningfDepth, WarninglnDepth
- Error, Errorf, Errorln, ErrorDepth, ErrorfDepth, ErrorlnDepth
- Fatal, Fatalf, Fatalln, FatalDepth, FatalfDepth, FatallnDepth
- Exit, Exitf, Exitln, ExitDepth, ExitfDepth, ExitlnDepth

**Structured logging:**

- InfoS, InfoSDepth — structured info with key-value pairs
- ErrorS, ErrorSDepth — structured error with key-value pairs

**V-leveled logging:**

- V(level) Verbose — returns Verbose for conditional logging
- VDepth(depth, level) Verbose

**Logger management:**

- SetLogger, SetLoggerWithOptions, SetSlogLogger, ClearLogger
- Background() Logger, TODO() Logger
- FromContext(ctx) Logger, NewContext(ctx, logger)
- LoggerWithValues, LoggerWithName
- EnableContextualLogging

**Object references:**

- KObj(obj) ObjectRef — structured reference to k8s object
- KObjs(slice) []ObjectRef — batch version
- KRef(namespace, name) ObjectRef — from strings
- KObjSlice — for lazy formatting
- Format(obj) — general formatting

**Output control:**

- InitFlags(flagset) — register klog flags
- Flush, FlushAndExit, StartFlushDaemon, StopFlushDaemon
- SetOutput, SetOutputBySeverity, LogToStderr
- CaptureState() State

**Utilities:**

- CopyStandardLogTo — redirect stdlib log
- NewStandardLogger — create stdlib logger backed by klog
- SafePtr — nil-safe pointer formatting

## Dependency Graph

**Direct dependencies:** `github.com/go-logr/logr` (type aliases), stdlib only

**Reverse dependencies:** 413+ packages across kubernetes org (near-universal dependency)

## Sub-packages

- `k8s.io/klog/v2/textlogger` — simpler output routing, own flags
- `k8s.io/klog/v2/ktesting` — per-test output for Go unit tests
- `k8s.io/klog/v2/klogr` — deprecated standalone logr.Logger
- `k8s.io/klog/v2/test` — reusable tests for logr.Logger implementations

## Notes

klog is a leaf dependency with zero k8s.io imports — it depends only on go-logr and stdlib. It is imported by virtually every other package in the ecosystem. The structured logging (InfoS/ErrorS) and contextual logging (FromContext/NewContext) APIs represent the modern usage pattern; the unstructured APIs (Info/Infof/etc.) are legacy.
